#!/usr/bin/env python3
"""Opt-in ACP/HTTP image and text smoke against authenticated real providers.

Requires acpdump, configured provider executables and account/network access.
All process/config/media artifacts are created in a temporary directory.
"""
import argparse
import base64
import json
import pathlib
import queue
import shlex
import struct
import subprocess
import threading
import time
import urllib.error
import urllib.request
import zlib
import tempfile


def png():
    width, height = 256, 128
    rows = b"".join(b"\0" + bytes([255, 0, 0]) * (width // 2)
                    + bytes([0, 0, 255]) * (width // 2) for _ in range(height))

    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b""))


class ACP:
    def __init__(self, command, cwd, log):
        self.log = open(log, "w", encoding="utf-8")
        self.process = subprocess.Popen(command, cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.log, text=True)
        self.messages = queue.Queue()
        self.next_id = 0
        self.frames = []

        def read():
            for line in self.process.stdout:
                try:
                    self.messages.put(json.loads(line))
                except ValueError:
                    continue
            self.messages.put(None)

        threading.Thread(target=read, daemon=True).start()

    def send(self, message):
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def call(self, method, params):
        self.next_id += 1
        call_id = self.next_id
        self.send({"jsonrpc": "2.0", "id": call_id, "method": method, "params": params})
        deadline = time.monotonic() + 120
        text = []
        while time.monotonic() < deadline:
            message = self.messages.get(timeout=max(.01, deadline - time.monotonic()))
            if message is None:
                raise RuntimeError("ACP process exited")
            self.frames.append(message)
            update = message.get("params", {}).get("update", {})
            if update.get("sessionUpdate") == "agent_message_chunk":
                content = update.get("content", {})
                if content.get("type") == "text":
                    text.append(content.get("text", ""))
            if message.get("id") == call_id and ("result" in message or "error" in message):
                return message, "".join(text)
            if "method" in message and "id" in message:
                if message["method"] == "session/request_permission":
                    self.send({"jsonrpc": "2.0", "id": message["id"], "result": {"outcome": {"outcome": "cancelled"}}})
                else:
                    self.send({"jsonrpc": "2.0", "id": message["id"], "error": {"code": -32601, "message": "unsupported"}})
        raise TimeoutError(method)

    def close(self):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        self.log.close()


def model_options(document):
    model = next(o for o in document["session"]["configOptions"] if o.get("category") == "model")

    def flatten(options):
        for option in options:
            if "options" in option:
                yield from flatten(option["options"])
            else:
                yield option["value"]

    return model["currentValue"], list(flatten(model["options"]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="bin/acpboss")
    parser.add_argument("--acpdump", default="acpdump")
    parser.add_argument("--provider", action="append", required=True, help="routing-id=ACP command")
    parser.add_argument("--model", action="append", default=[], help="routing-id=upstream model (default: advertised current model)")
    args = parser.parse_args()
    binary = str(pathlib.Path(args.binary).resolve())
    providers = dict(value.split("=", 1) for value in args.provider)
    overrides = dict(value.split("=", 1) for value in args.model)
    root = pathlib.Path(tempfile.mkdtemp(prefix="acpboss-media-smoke-"))
    print("artifacts:", root, flush=True)
    image = png()
    (root / "image.png").write_bytes(image)
    data = base64.b64encode(image).decode()
    image_prompt = ("Identify the solid color of the LEFT half and the RIGHT half of the supplied image. "
                    "Reply exactly LEFT=<COLOR> RIGHT=<COLOR> using uppercase basic color names. "
                    "Do not read files, use tools or modify anything.")
    text_prompt = "Reply with exactly ACPBOSS_OK. Do not read files, use tools or modify anything."
    config = {"runtime": {"providers": {key: {"cmd": command} for key, command in providers.items()}},
              "acpboss": {"working_dir": str(root), "http": {"listen": "127.0.0.1:0"},
                          "startup_timeout": "90s", "request_timeout": "120s", "shutdown_timeout": "10s"}}
    config_path = root / "config.yaml"
    config_path.write_text(json.dumps(config), encoding="utf-8")
    models = {}
    capabilities = {}
    results = []

    def record(transport, provider, kind, text="", status="failed", error=None):
        expected = "LEFT=RED RIGHT=BLUE" if kind == "image" else "ACPBOSS_OK"
        passed = status == "completed" and text.strip() == expected
        result = {"transport": transport, "provider": provider, "model": models.get(provider),
                  "kind": kind, "status": status, "text": text, "passed": passed}
        if error:
            result["error"] = error
        results.append(result)
        (root / "summary.json").write_text(json.dumps(results, indent=2), encoding="utf-8")
        print(json.dumps(result), flush=True)

    for provider, command in providers.items():
        dump = subprocess.run([args.acpdump, "--json", "--", *shlex.split(command)], cwd=root,
                              capture_output=True, text=True, timeout=120, check=True)
        document = json.loads(dump.stdout)
        (root / (provider + "-direct.json")).write_text(json.dumps(document, indent=2), encoding="utf-8")
        current, available = model_options(document)
        models[provider] = overrides.get(provider, current)
        if models[provider] not in available:
            raise RuntimeError("selected model missing from " + provider + " catalog")
        capabilities[provider] = document["initialize"].get("agentCapabilities", {}).get("promptCapabilities", {})
        print(provider, "capabilities:", capabilities[provider], "model:", models[provider], flush=True)

    dump = subprocess.run([args.acpdump, "--json", "--", binary, "stdio", "--config", str(config_path)],
                          cwd=root, capture_output=True, text=True, timeout=150, check=True)
    (root / "proxy.json").write_text(dump.stdout, encoding="utf-8")
    if not json.loads(dump.stdout)["initialize"]["agentCapabilities"].get("promptCapabilities", {}).get("image"):
        raise RuntimeError("proxy did not advertise available image capability")

    for transport in ["direct-acp", "acpboss-acp"]:
        for provider, command in providers.items():
            cmd = shlex.split(command) if transport == "direct-acp" else [binary, "stdio", "--config", str(config_path)]
            client = ACP(cmd, root, root / (transport + "-" + provider + ".stderr"))
            try:
                init, _ = client.call("initialize", {"protocolVersion": 1, "clientCapabilities": {},
                                                     "clientInfo": {"name": "acpboss-smoke", "version": "1"}})
                if "error" in init:
                    raise RuntimeError("ACP initialization failed")
                for kind in ["image", "text"]:
                    if kind == "image" and not capabilities[provider].get("image"):
                        record(transport, provider, kind, status="unsupported")
                        continue
                    session, _ = client.call("session/new", {"cwd": str(root), "mcpServers": []})
                    sid = session["result"]["sessionId"]
                    selected = provider + "/" + models[provider] if transport == "acpboss-acp" else models[provider]
                    selection, _ = client.call("session/set_config_option", {"sessionId": sid, "configId": "model", "value": selected})
                    if "error" in selection:
                        record(transport, provider, kind, error=selection["error"])
                        continue
                    blocks = [{"type": "text", "text": image_prompt if kind == "image" else text_prompt}]
                    if kind == "image":
                        blocks.append({"type": "image", "mimeType": "image/png", "data": data})
                    try:
                        answer, text = client.call("session/prompt", {"sessionId": sid, "prompt": blocks})
                        status = "completed" if answer.get("result", {}).get("stopReason") == "end_turn" else "failed"
                        record(transport, provider, kind, text, status, answer.get("error"))
                    except (RuntimeError, TimeoutError, queue.Empty) as error:
                        record(transport, provider, kind, error=type(error).__name__)
                    if transport == "acpboss-acp":
                        client.call("session/close", {"sessionId": sid})
            finally:
                client.close()
                (root / (transport + "-" + provider + ".json")).write_text(json.dumps(client.frames, indent=2), encoding="utf-8")

    log = open(root / "http.stderr", "w", encoding="utf-8")
    process = subprocess.Popen([binary, "api", "--config", str(config_path)], cwd=root,
                               stdout=subprocess.DEVNULL, stderr=log)
    try:
        address = None
        for _ in range(200):
            for line in (root / "http.stderr").read_text(encoding="utf-8").splitlines():
                if line.startswith("acpboss listening on "):
                    address = line.removeprefix("acpboss listening on ")
            if address:
                break
            if process.poll() is not None:
                raise RuntimeError("HTTP process exited")
            time.sleep(.5)
        if not address:
            raise TimeoutError("HTTP startup")
        base = "http://" + address
        catalog = json.load(urllib.request.urlopen(base + "/v1/models", timeout=20))
        (root / "http-models.json").write_text(json.dumps(catalog, indent=2), encoding="utf-8")
        for provider in providers:
            for kind in ["image", "text"]:
                if kind == "image" and not capabilities[provider].get("image"):
                    record("acpboss-http", provider, kind, status="unsupported")
                    continue
                content = [{"type": "input_text", "text": image_prompt}]
                if kind == "image":
                    content.append({"type": "input_image", "image_url": "data:image/png;base64," + data, "detail": "auto"})
                body = {"model": provider + "/" + models[provider], "store": False,
                        "input": [{"role": "user", "content": content}] if kind == "image" else text_prompt}
                request = urllib.request.Request(base + "/v1/responses", data=json.dumps(body).encode(),
                                                 headers={"Content-Type": "application/json"})
                try:
                    with urllib.request.urlopen(request, timeout=135) as response:
                        answer = json.load(response)
                    text = "".join(part.get("text", "") for item in answer.get("output", [])
                                   for part in item.get("content", []) if part.get("type") == "output_text")
                    record("acpboss-http", provider, kind, text, answer.get("status"), answer.get("error"))
                except (urllib.error.URLError, TimeoutError) as error:
                    record("acpboss-http", provider, kind, error=str(error))
    finally:
        process.terminate()
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        log.close()
    return 0 if all(result["passed"] or result["status"] == "unsupported" for result in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
