#!/usr/bin/env python3
"""Scratch-only NeoPi role proof. Never reads an existing NeoPi profile.

prepare authenticates an upstream catalogue and writes private proxy configuration.
run requires explicit integrated A/B/C delivery SHAs and a supplied proxy binary.
Only a verified real turn for every wire makes receipt.status equal to passed.
"""

import argparse
import contextlib
import hashlib
import http.client
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import threading
import time
import tomllib
import urllib.error
import urllib.parse
import urllib.request

SCRATCH = Path('/mnt/alias/m2/agents/scratch/neoproxy-backend-e')
WIRES = {
    'chat': ('openai-completions', '/v1/chat/completions'),
    'responses': ('openai-responses', '/v1/responses'),
    'messages': ('anthropic-messages', '/v1/messages'),
}
REAL_PROVIDER = 'openai-compatible-proof-real'
FAULT_PROVIDER_PREFIX = 'openai-compatible-proof-fault-'
HOP_HEADERS = {'connection', 'keep-alive', 'proxy-authenticate',
               'proxy-authorization', 'te', 'trailer', 'transfer-encoding', 'upgrade'}


class ProofError(Exception):
    """A prerequisite or evidence requirement is not satisfied."""


def private_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open('w', encoding='utf-8') as output:
        os.chmod(path, 0o600)
        json.dump(value, output, indent=2)
        output.write('\n')


def private_toml(path, value):
    def literal(item):
        if isinstance(item, dict):
            return '{ ' + ', '.join(json.dumps(key) + ' = ' + literal(child)
                                   for key, child in item.items()) + ' }'
        if isinstance(item, list):
            return '[' + ', '.join(literal(child) for child in item) + ']'
        return json.dumps(item)

    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open('w', encoding='utf-8') as output:
        os.chmod(path, 0o600)
        for key, item in value.items():
            output.write(json.dumps(key) + ' = ' + literal(item) + '\n')


def redact_bytes(raw, sensitive):
    for value in sensitive:
        if value:
            raw = raw.replace(value.encode(), b'[REDACTED]')
    return raw


def redact_json(value, sensitive):
    return json.loads(redact_bytes(json.dumps(value).encode(), sensitive))


def capture_log(source, destination, sensitive):
    with source:
        for line in source:
            destination.write(redact_bytes(line, sensitive))
            destination.flush()


def scratch_path(value):
    path = Path(value).resolve()
    if not path.is_relative_to(SCRATCH.resolve()) or path == SCRATCH.resolve():
        raise ProofError(f'run directory must be a fresh child of {SCRATCH}')
    return path


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def safe_environment(home):
    """Allowlist, rather than inherit profile/broker/plugin/credential variables."""
    return {
        'HOME': str(home), 'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8',
        'XDG_CONFIG_HOME': str(home / '.config'),
        'XDG_CACHE_HOME': str(home / '.cache'),
        'XDG_DATA_HOME': str(home / '.local/share'),
        'TMPDIR': str(home / 'tmp'),
        'PI_CODING_AGENT_DIR': str(home / 'agent'),
        'PI_NO_TITLE': '1', 'PI_NO_PTY': '1',
    }


class API:
    def __init__(self, base, operator):
        self.base = base
        self.operator = operator
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(self.cookies))

    def call(self, method, route, body=None, operator=False, key=None):
        headers = {'Content-Type': 'application/json', 'Origin': self.base, 'X-Neoproxy-CSRF': '1'}
        if operator or key:
            headers['Authorization'] = 'Bearer ' + (self.operator if operator else key)
        request = urllib.request.Request(
            self.base + route, data=None if body is None else json.dumps(body).encode(),
            headers=headers, method=method)
        try:
            with self.opener.open(request) as response:
                raw = response.read()
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as error:
            # Error bodies may contain upstream credentials: do not print them.
            raise ProofError(f'{method} {route} returned HTTP {error.code}') from None


def prepare(args):
    root = scratch_path(args.scratch)
    if root.exists():
        raise ProofError('refusing to overwrite an existing run directory')
    key = os.environ.get(args.upstream_key_env, '').strip()
    if not key:
        raise ProofError(f'missing upstream credential environment variable {args.upstream_key_env}')
    if urllib.parse.urlsplit(args.upstream_base).scheme != 'https':
        raise ProofError('successful fallback must use an HTTPS real upstream, not a local fixture')
    upstream_host = urllib.parse.urlsplit(args.upstream_base).hostname
    if upstream_host in {'localhost', '127.0.0.1', '::1'}:
        raise ProofError('localhost cannot be the successful fallback')
    request = urllib.request.Request(args.upstream_base.rstrip('/') + '/models', headers={
        'Authorization': 'Bearer ' + key, 'User-Agent': 'NeoProxy-E-local-proof'})
    try:
        with urllib.request.urlopen(request) as response:
            catalogue = json.load(response)
    except urllib.error.HTTPError as error:
        raise ProofError(f'upstream authenticated catalogue returned HTTP {error.code}') from None
    ids = [entry['id'] for entry in catalogue.get('data', [])]
    if args.upstream_model not in ids:
        raise ProofError('selected real upstream model is absent from authenticated catalogue')
    root.mkdir(parents=True, mode=0o700)
    operator = secrets.token_hex(32)
    port, fault_port, observer_port = free_port(), free_port(), free_port()
    providers = []
    for wire in WIRES:
        providers.append({
            'name': 'proof-fault-' + wire,
            'base-url': f'http://127.0.0.1:{fault_port}/v1',
            'request-retry': 0, 'keys': [{'api-key': 'fault-injection-only-' + wire}],
            'models': [{'name': 'fail-' + wire, 'alias': 'fail-' + wire}],
        })
    providers.append({
        'name': 'proof-real', 'base-url': args.upstream_base.rstrip('/'),
        'request-retry': 0, 'keys': [{'api-key': key}],
        'models': [{'name': args.upstream_model, 'alias': 'fallback',
                    'thinking': {'levels': []}}],
    })
    config = {
        'config-version': 8,
        'server': {'host': '127.0.0.1', 'port': port, 'discovery': {'enabled': False}},
        'management': {'allow-remote': False, 'secret-key': operator,
                       'disable-control-panel': True, 'disable-auto-update-panel': True},
        'access': {'api-keys': [secrets.token_hex(32)]},
        'oauth': {'auth-dir': str(root / 'proxy-auth')},
        'api-keys': {'openai-compatibility': providers},
        'routing': {'retry': {'request-retry': 0, 'max-retry-interval': 0}},
        'observability': {'logs': {'debug': False, 'logging-to-file': False, 'request-log': False}},
    }
    private_toml(root / 'proxy-config.toml', config)
    private_json(root / 'private.json', {'operator': operator, 'password': secrets.token_hex(24)})
    manifest = {
        'scratch': str(root), 'proxy_port': port, 'fault_port': fault_port,
        'observer_port': observer_port, 'upstream_base': args.upstream_base,
        'upstream_model': args.upstream_model,
        'credential_source': 'inherited environment ' + args.upstream_key_env,
        'authenticated_upstream_catalogue': True,
        'prepared_at': int(time.time() * 1000),
    }
    private_json(root / 'setup.json', manifest)
    print(json.dumps({'status': 'prepared', 'scratch': str(root),
                      'real_upstream_model': args.upstream_model,
                      'authenticated_catalogue': True}))


class FaultHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
        record = {'at': time.time_ns(), 'path': self.path, 'model': payload.get('model'),
                  'status': 503, 'reason': 'deliberate first-stage upstream failure'}
        self.server.records.append(record)
        raw = json.dumps({'error': {'message': record['reason'], 'type': 'server_error'}}).encode()
        self.send_response(503)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_args):
        pass


class ObserveHandler(http.server.BaseHTTPRequestHandler):
    """Transparent transport observation; never synthesizes an inference response."""
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        payload = json.loads(body)
        headers = {name: value for name, value in self.headers.items()
                   if name.lower() not in HOP_HEADERS | {'host', 'content-length'}}
        connection = http.client.HTTPConnection('127.0.0.1', self.server.proxy_port)
        try:
            connection.request('POST', self.path, body, headers)
            upstream = connection.getresponse()
            record = {'path': self.path, 'model': payload.get('model'),
                      'client': self.headers.get('X-Client'), 'status': upstream.status,
                      'request_id': upstream.getheader('X-NeoProxy-Request-ID')}
            self.server.records.append(record)
            self.send_response(upstream.status)
            for name, value in upstream.getheaders():
                if name.lower() not in HOP_HEADERS | {'content-length'}:
                    self.send_header(name, value)
            self.send_header('Connection', 'close')
            self.end_headers()
            while chunk := upstream.read1(65536):
                self.wfile.write(chunk)
                self.wfile.flush()
            self.close_connection = True
        finally:
            connection.close()

    def log_message(self, *_args):
        pass


@contextlib.contextmanager
def local_server(port, handler, **attributes):
    server = http.server.ThreadingHTTPServer(('127.0.0.1', port), handler)
    server.records = []
    for key, value in attributes.items():
        setattr(server, key, value)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def discover_virtual(models, wanted):
    ids = [entry['id'] for entry in models.get('data', [])
           if entry.get('id', '').startswith('neoproxy/')
           and entry['id'].count('/') == 1]
    if wanted not in ids:
        raise ProofError(f'virtual selector {wanted} was not discovered in authenticated /v1/models')
    return wanted


def assistant_result(raw):
    messages = []
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get('type') == 'message_end' and event.get('message', {}).get('role') == 'assistant':
            messages.append(event['message'])
    if not messages:
        raise ProofError('NeoPi emitted no completed assistant message')
    message = messages[-1]
    if message.get('stopReason') in {'error', 'aborted'}:
        raise ProofError('NeoPi assistant turn ended in failure')
    text = '\n'.join(block.get('text', '') for block in message.get('content', [])
                     if block.get('type') == 'text').strip()
    if not text:
        raise ProofError('NeoPi completed without a real textual answer')
    return {'text': text, 'usage': message.get('usage'),
            'provider': message.get('provider'), 'model': message.get('model'),
            'stop_reason': message.get('stopReason')}


def validate_evidence(wire, selector, observed, fault, request, assistant, fallback_account):
    protocol, endpoint = WIRES[wire]
    if observed.get('path') != endpoint or observed.get('model') != selector:
        raise ProofError(f'{protocol}: observed wire endpoint/model mismatch')
    if observed.get('status') != 200 or not observed.get('request_id'):
        raise ProofError(f'{protocol}: missing successful correlated proxy transport')
    if observed['request_id'] != request.get('id') or request.get('asked') != selector:
        raise ProofError(f'{protocol}: request record correlation mismatch')
    decision = request.get('decision', {})
    trail = decision.get('trail', [])
    if not any(step.get('skipped') == 'upstream-error' and step.get('index') == 0 for step in trail):
        raise ProofError(f'{protocol}: persisted trail lacks first-stage upstream-error')
    if not any(item.get('status') == 503 and item.get('model') == 'fail-' + wire for item in fault):
        raise ProofError(f'{protocol}: first-stage upstream was not actually called and failed')
    if decision.get('stageIndex') != 1 or decision.get('accountId') != fallback_account:
        raise ProofError(f'{protocol}: fallback stage/account was not selected')
    if decision.get('model') != REAL_PROVIDER + '/fallback' or request.get('outcome') != 'ok':
        raise ProofError(f'{protocol}: no real configured fallback success')
    tokens = request.get('tokens', {})
    if tokens.get('in', 0) <= 0 or tokens.get('out', 0) <= 0 or not assistant.get('usage'):
        raise ProofError(f'{protocol}: actual provider usage missing')
    if assistant.get('model') != selector:
        raise ProofError(f'{protocol}: NeoPi did not retain the discovered virtual selector')


def profile(root, wire, protocol, selector, port, key):
    home = root / ('client-' + wire)
    for directory in [home, home / 'agent', home / 'tmp', home / 'workspace']:
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    provider = 'neoproxy-proof-' + wire
    model = {'id': selector, 'name': 'NeoProxy isolated proof', 'reasoning': False,
             'input': ['text'], 'contextWindow': 32768, 'maxTokens': 512,
             'compat': {'supportsReasoningEffort': False, 'supportsDeveloperRole': False}}
    # NeoPi models currently accept JSON/JSONC but not TOML.
    private_json(home / 'agent/models.json', {'providers': {provider: {
        'baseUrl': f'http://127.0.0.1:{port}/v1', 'apiKey': 'NEOPROXY_PROOF_KEY',
        'api': protocol, 'auth': 'apiKey', 'authHeader': True,
        'headers': {'X-Client': 'neoproxy-e2e-' + wire}, 'models': [model]}}})
    role = 'proof-' + wire
    settings = {'modelRoles': {role: provider + '/' + selector},
                'enabledModels': [provider + '/' + selector],
                'providers': {'openaiWebsockets': 'off'},
                'retry': {'enabled': False, 'modelFallback': False,
                          'fallbackChains': {role: [], 'default': []}},
                'includeWorkspaceTree': False, 'memory': {'backend': 'off'},
                'disabledProviders': ['native', 'claude', 'codex', 'gemini', 'github',
                                      'opencode', 'cursor', 'agents-md']}
    private_toml(home / 'agent/config.toml', settings)
    environment = safe_environment(home)
    environment['NEOPROXY_PROOF_KEY'] = key
    return home, role, settings, environment


def bootstrap(api, manifest, password):
    admin = api.call('POST', '/v8/session/bootstrap',
                     {'handle': 'proof-admin', 'name': 'Isolated proof admin', 'password': password},
                     operator=True)
    vault = api.call('GET', '/v8/neo/vault')
    accounts = {}
    for account in vault['accounts']:
        if account['providerId'].startswith('openai-compatible-proof-'):
            accounts[account['providerId']] = account['id']
            api.call('PATCH', '/v8/neo/vault/accounts/' + urllib.parse.quote(account['id'], safe=''),
                     {'ownerId': admin['userId']})
            api.call('PUT', '/v8/neo/vault/accounts/' + urllib.parse.quote(account['id'], safe='') + '/windows/day',
                     {'capacityTokens': 1000000, 'startsAt': int(time.time() * 1000)})
    expected = {REAL_PROVIDER} | {FAULT_PROVIDER_PREFIX + wire for wire in WIRES}
    if set(accounts) != expected:
        raise ProofError('proxy account inventory did not register all isolated configured upstreams')
    grants, virtuals = [], {}
    for wire in WIRES:
        stages = []
        for index, provider, model in [(0, FAULT_PROVIDER_PREFIX + wire, 'fail-' + wire),
                                       (1, REAL_PROVIDER, 'fallback')]:
            stages.append({'id': 's' + str(index), 'model': provider + '/' + model,
                           'accounts': [accounts[provider]], 'draw': 'fill',
                           'perAccount': 1, 'cap': 1, 'window': 'day', 'per': 'user', 'effort': None})
        virtual = api.call('POST', '/v8/neo/virtual-models', {
            'name': 'proof-' + wire, 'description': 'Isolated real fallback proof',
            'stages': stages, 'terminal': {'kind': 'limit', 'message': 'proof stages unavailable'}})
        grants.append({'virtualModel': virtual['id'], 'allotment': 1})
        virtuals[wire] = virtual
    user = api.call('POST', '/v8/neo/users', {
        'handle': 'proof-client', 'name': 'Isolated NeoPi inference client',
        'role': 'service', 'state': 'active', 'providerAccess': [], 'grants': grants,
        'limits': {'rpm': None, 'tokensPerDay': None}})
    issued = api.call('POST', '/v8/neo/users/' + user['id'] + '/keys',
                      {'label': 'isolated-neopi-role-proof', 'accountIds': list(accounts.values())})
    return accounts, virtuals, issued['secret']


def run(args):
    root = scratch_path(args.scratch)
    manifest = json.loads((root / 'setup.json').read_text())
    credentials = json.loads((root / 'private.json').read_text())
    configuration = tomllib.loads((root / 'proxy-config.toml').read_text())
    sensitive = [credentials['operator'], credentials['password']]
    sensitive.extend(configuration['access']['api-keys'])
    sensitive.extend(entry['api-key']
                     for group in configuration['api-keys']['openai-compatibility']
                     for entry in group['keys'])
    if (root / 'receipt.json').exists():
        raise ProofError('run already attempted; prepare a fresh directory instead of reusing proof state')
    binary = Path(args.proxy_binary).resolve(strict=True)
    npi = Path(args.npi).resolve(strict=True)
    with binary.open('rb') as executable:
        binary_sha256 = hashlib.file_digest(executable, 'sha256').hexdigest()
    receipt = {'status': 'not-passed', 'proxy_build_sha': args.proxy_sha,
               'proxy_binary_sha256': binary_sha256,
               'interface_deliveries': {'A': args.a_sha, 'B': args.b_sha, 'C': args.c_sha},
               'scratch': str(root), 'upstream': {'base': manifest['upstream_base'],
               'model': manifest['upstream_model'], 'credential_source': manifest['credential_source']},
               'isolation': 'fresh HOME/XDG/TMPDIR/PI_CODING_AGENT_DIR and allowlisted environment; no owner profile read',
               'protocols': []}
    home = root / 'proxy-home'
    (home / 'tmp').mkdir(parents=True, mode=0o700)
    environment = safe_environment(home)
    environment['NEOPROXY_DATA_DIR'] = str(root / 'proxy-data')
    command = [str(binary), '--config', str(root / 'proxy-config.toml'), '--local-model']
    receipt['proxy_command'] = command
    api = API(f'http://127.0.0.1:{manifest["proxy_port"]}', credentials['operator'])
    try:
        with local_server(manifest['fault_port'], FaultHandler) as faults, \
                local_server(manifest['observer_port'], ObserveHandler,
                             proxy_port=manifest['proxy_port']) as observer, \
                (root / 'proxy.log').open('wb') as proxy_log:
            process = subprocess.Popen(command, cwd=root, env=environment,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            log_thread = threading.Thread(target=capture_log,
                                          args=(process.stdout, proxy_log, sensitive), daemon=True)
            log_thread.start()
            try:
                for _ in range(300):
                    if process.poll() is not None:
                        raise ProofError('isolated proxy exited at startup; private proxy.log holds details')
                    try:
                        with socket.create_connection(('127.0.0.1', manifest['proxy_port']), timeout=0.1):
                            break
                    except OSError:
                        time.sleep(0.1)
                else:
                    raise ProofError('isolated proxy did not open its listener')
                accounts, virtuals, key = bootstrap(api, manifest, credentials['password'])
                sensitive.append(key)
                sensitive.extend(cookie.value for cookie in api.cookies)
                models = api.call('GET', '/v1/models', key=key)
                receipt['discovered_models'] = models
                for wire, (protocol, _) in WIRES.items():
                    selector = discover_virtual(models, virtuals[wire]['served'])
                    client_home, role, settings, client_env = profile(
                        root, wire, protocol, selector, manifest['observer_port'], key)
                    client_command = [str(npi), '--print', '--mode', 'json', '--no-session',
                                      '--no-title', '--no-tools', '--no-lsp', '--no-extensions',
                                      '--no-skills', '--no-mcp', '--no-rules', '--chat=raw',
                                      '--model', '@' + role, '--thinking', 'off',
                                      'Reply with one short sentence naming a planet and one fact about it.']
                    turn = subprocess.run(client_command, cwd=client_home / 'workspace',
                                          env=client_env, input=b'', capture_output=True)
                    (client_home / 'stdout.jsonl').write_bytes(redact_bytes(turn.stdout, sensitive))
                    (client_home / 'stderr.log').write_bytes(redact_bytes(turn.stderr, sensitive))
                    if turn.returncode:
                        raise ProofError(f'{protocol}: actual NeoPi turn exited {turn.returncode}; private captures retained')
                    assistant = assistant_result(turn.stdout.decode())
                    observed = [item for item in observer.records if item['client'] == 'neoproxy-e2e-' + wire]
                    if len(observed) != 1:
                        raise ProofError(f'{protocol}: expected one role-selected inference request, observed {len(observed)}')
                    record = api.call('GET', '/v8/neo/requests/' + urllib.parse.quote(observed[0]['request_id'] or '', safe=''))
                    validate_evidence(wire, selector, observed[0], faults.records, record,
                                      assistant, accounts[REAL_PROVIDER])
                    receipt['protocols'].append({
                        'protocol': protocol, 'selector': selector, 'role': role,
                        'role_configuration': settings['modelRoles'],
                        'scratch_profile': str(client_home / 'agent'), 'command': client_command,
                        'request_correlation': observed[0],
                        'first_stage_failure': [item for item in faults.records if item['model'] == 'fail-' + wire],
                        'fallback_concrete_upstream_model': manifest['upstream_model'],
                        'fallback_proxy_model': record['decision']['model'],
                        'fallback_account': accounts[REAL_PROVIDER],
                        'request': record, 'actual_assistant': assistant})
                    private_json(root / 'receipt.json', redact_json(receipt, sensitive))
                receipt['status'] = 'passed'
            finally:
                process.terminate()
                process.wait()
                log_thread.join()
                private_json(root / 'fault-observations.json', faults.records)
                private_json(root / 'transport-observations.json', observer.records)
    except Exception as error:
        receipt['blocker'] = str(error) if isinstance(error, ProofError) else type(error).__name__
        raise
    finally:
        private_json(root / 'receipt.json', redact_json(receipt, sensitive))
    print(json.dumps({'status': receipt['status'], 'receipt': str(root / 'receipt.json'),
                      'protocols_passed': len(receipt['protocols'])}))


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='action', required=True)
    preparation = commands.add_parser('prepare')
    preparation.add_argument('--scratch', required=True)
    preparation.add_argument('--upstream-key-env', default='GROQ_API_KEY')
    preparation.add_argument('--upstream-base', default='https://api.groq.com/openai/v1')
    preparation.add_argument('--upstream-model', default='openai/gpt-oss-20b')
    preparation.set_defaults(func=prepare)
    execution = commands.add_parser('run')
    execution.add_argument('--scratch', required=True)
    execution.add_argument('--proxy-binary', required=True)
    execution.add_argument('--proxy-sha', required=True)
    execution.add_argument('--npi', default=shutil.which('npi'))
    for slice_name in ('a', 'b', 'c'):
        execution.add_argument('--' + slice_name + '-sha', required=True,
                               help='explicit delivered interface commit; do not run before delivery')
    execution.set_defaults(func=run)
    args = parser.parse_args()
    try:
        args.func(args)
    except (ProofError, OSError, ValueError) as error:
        # Never print arbitrary transport/configuration exceptions or secret-bearing bodies.
        detail = str(error) if isinstance(error, ProofError) else type(error).__name__
        print(json.dumps({'status': 'not-passed', 'blocker': detail}), file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
