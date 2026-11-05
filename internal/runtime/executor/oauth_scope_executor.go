package executor

import cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

// ForAPIKey binds configuration once after credential selection. Value receivers
// copy executor configuration while preserving shared transport/session stores.
func (e CodexExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e ClaudeExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e GeminiExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e GeminiVertexExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e OpenAICompatExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e XAIExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

// ForAPIKey keeps the Z.ai routing: the promoted ClaudeExecutor scope would
// otherwise replace the executor with its Anthropic-only half.
func (e ZaiExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	e.ClaudeExecutor = e.ClaudeExecutor.ForAPIKey().(*ClaudeExecutor)
	e.counter = e.counter.ForAPIKey().(*OpenAICompatExecutor)
	return &e
}

// ForAPIKey keeps the Kimi Code routing for the same reason as Z.ai.
func (e KimiExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	e.ClaudeExecutor = e.ClaudeExecutor.ForAPIKey().(*ClaudeExecutor)
	e.counter = e.counter.ForAPIKey().(*OpenAICompatExecutor)
	return &e
}

func (e CodexWebsocketsExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	if e.CodexExecutor != nil {
		e.CodexExecutor = e.CodexExecutor.ForAPIKey().(*CodexExecutor)
	}
	return &e
}

func (e CodexAutoExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	if e.httpExec != nil {
		e.httpExec = e.httpExec.ForAPIKey().(*CodexExecutor)
	}
	if e.wsExec != nil {
		e.wsExec = e.wsExec.ForAPIKey().(*CodexWebsocketsExecutor)
	}
	return &e
}
