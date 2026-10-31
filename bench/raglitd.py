"""A throwaway raglit daemon for a bench run.

raglit has no in-process mode: every client goes through a daemon, and the
daemon always requires a token. A bench must never touch the user's daemon, so
it starts its own on a fresh root and a free loopback port, writes the model
config into that root, and stops it on exit. Clients on loopback read the
daemon's admin token themselves; HTTP calls from Python use daemon.token.
"""
import atexit, json, os, socket, subprocess, time, urllib.error, urllib.request
from pathlib import Path


class Daemon:
    def __init__(self, raglit, root, config, embed=False, log=None):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)
        (self.root / "config.json").write_text(json.dumps(config, indent=1))
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        self.base = f"http://127.0.0.1:{port}"
        # RAGLIT_CHILD / RAGLIT_NO_AUTOBUILD: never rebuild from a checkout and
        # never hand off to the user's systemd unit.
        self.env = {**os.environ, "RAGLIT_ROOT": str(self.root), "RAGLIT_CHILD": "1",
                    "RAGLIT_NO_AUTOBUILD": "1", "RAGLIT_DAEMON": self.base}
        self.env.pop("RAGLIT_TOKEN", None)
        cmd = [raglit, "daemon", "--root", str(self.root), "--addr", f"127.0.0.1:{port}"]
        if embed:
            cmd.append("--embed")
        out = open(log or self.root / "daemon.log", "a")
        self.proc = subprocess.Popen(cmd, env=self.env, stdout=out, stderr=out)
        atexit.register(self.stop)
        for _ in range(200):
            try:
                with urllib.request.urlopen(self.base + "/api/health", timeout=1) as r:
                    self.token = (self.root / "admin.token").read_text().strip()
                    return
            except (OSError, urllib.error.URLError):
                if self.proc.poll() is not None:
                    raise RuntimeError(f"raglit daemon exited; see {out.name}")
                time.sleep(0.1)
        raise RuntimeError(f"raglit daemon did not come up at {self.base}")

    def get(self, path):
        req = urllib.request.Request(self.base + path, headers={"Authorization": "Bearer " + self.token})
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.loads(r.read())

    def stop(self):
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
