#!/usr/bin/env python3
"""Append Docker/Compose environment to FieldPulse debug log (session 6cb197). Run on the host where docker works."""
import json
import os
import subprocess
import time

LOG = os.path.join(
    os.path.dirname(__file__), "..", "..", ".cursor", "debug-6cb197.log"
)


def sh(cmd: str) -> str:
    try:
        return subprocess.check_output(cmd, shell=True, stderr=subprocess.STDOUT, timeout=30).decode(
            "utf-8", errors="replace"
        ).strip()
    except subprocess.CalledProcessError as e:
        return (e.output or b"").decode("utf-8", errors="replace").strip()
    except Exception as e:
        return str(e)


def line(hypothesis_id: str, message: str, data: dict) -> None:
    os.makedirs(os.path.dirname(LOG), exist_ok=True)
    payload = {
        "sessionId": "6cb197",
        "hypothesisId": hypothesis_id,
        "message": message,
        "data": data,
        "timestamp": int(time.time() * 1000),
    }
    with open(LOG, "a", encoding="utf-8") as f:
        f.write(json.dumps(payload, ensure_ascii=False) + "\n")


def main() -> None:
    # H1: Compose v1 vs Docker Engine API mismatch
    line(
        "H1",
        "versions",
        {
            "docker": sh("docker --version"),
            "compose_py": sh("docker-compose --version"),
            "compose_v2": sh("docker compose version"),
        },
    )
    # H2: Image inspect shape (ContainerConfig presence in API JSON)
    imgs = sh("docker images --format '{{.Repository}}:{{.Tag}}' 2>&1").splitlines()
    sample = next((i for i in imgs if "device-service" in i or "fieldpulse" in i), "")
    if sample:
        raw = sh(f"docker image inspect {sample} 2>&1")
        line(
            "H2",
            "image_inspect",
            {
                "sample_image": sample,
                "raw_has_ContainerConfig": "ContainerConfig" in raw,
                "raw_has_Config": '"Config"' in raw,
                "snippet": raw[:2500],
            },
        )
    else:
        line("H2", "image_inspect_skipped", {"reason": "no fieldpulse image found"})


if __name__ == "__main__":
    main()
