// Every capture script that loads a NeoPi extension runs under a scratch HOME
// it creates itself: NeoPi's extensions write under the home directory (the
// Marineris login saves its gateway address there), and a capture must never
// touch a real profile. The script re-launches itself as a child whose HOME is
// a fresh mkdtemp directory, never one the caller supplied, then the child
// verifies (symlinks resolved) that the directory is the one the parent made.
import { existsSync, lstatSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join, sep } from "node:path";

const MARKER = ".capture-scratch-home";

export async function ensureScratchHome(): Promise<void> {
	const token = process.env.CAPTURE_SCRATCH_TOKEN;
	if (token === undefined) {
		const dir = realpathSync(mkdtempSync(join(tmpdir(), "capture-home-")));
		const fresh = crypto.randomUUID();
		writeFileSync(join(dir, MARKER), fresh);
		const child = Bun.spawn([process.execPath, "--preserve-symlinks", ...process.argv.slice(1)], {
			env: { ...process.env, HOME: dir, CAPTURE_SCRATCH_TOKEN: fresh, CAPTURE_CALLER_HOME: realpathSync(homedir()) },
			stdin: "inherit",
			stdout: "inherit",
			stderr: "inherit",
		});
		const code = await child.exited;
		rmSync(dir, { recursive: true, force: true });
		process.exit(code);
	}
	const home = realpathSync(homedir());
	// Bun's userInfo() honours $HOME, so the account's real home comes from the
	// passwd database and from the caller's own home.
	const realHomes = new Set<string>();
	const passwd = Bun.spawnSync(["getent", "passwd", String(process.getuid?.() ?? "")]).stdout.toString().trim().split(":");
	for (const candidate of [passwd[5], process.env.CAPTURE_CALLER_HOME]) {
		try {
			if (candidate) realHomes.add(realpathSync(candidate));
		} catch {}
	}
	const scratchRoot = realpathSync(tmpdir());
	const marker = join(home, MARKER);
	if (
		realHomes.has(home) ||
		!home.startsWith(scratchRoot + sep) ||
		!existsSync(marker) ||
		readFileSync(marker, "utf8") !== token ||
		(existsSync(join(home, ".omp")) && (lstatSync(join(home, ".omp")).isSymbolicLink() || !realpathSync(join(home, ".omp")).startsWith(home + sep)))
	) {
		throw new Error(`refusing to run: ${home} is not the scratch home this capture created`);
	}
}
