# Printer Hawk — Dual-Stage VLM Print Failure Guard for Prusa Connect Local

**Printer Hawk** is a native Prusa Connect Local extension (`com.github.aux.ln4cy.printer-hawk`) that monitors 3D prints with Vision-Language Models (VLMs) instead of single-frame spaghetti-only heuristics. It works with **any combination** of local GPU/CPU inference (**Ollama**, **LM Studio**, **vLLM**, **llama.cpp**) and cloud APIs (**Gemini**, **Anthropic Claude**, **OpenAI / OpenRouter**), with automatic primary -> fallback failover.

## Key Features

- **Dual-Stage AI Verification (with Failover):**
  - **Stage 1 (Screener):** Runs every layer or fallback interval with previous + current frames and live printer telemetry (Z height, nozzle/bed temps, active tool, progress). Automatically fails over from Stage 1 Primary -> Stage 1 Fallback if the primary is unreachable or busy.
  - **Stage 2 (Confirmation Gate):** Triggered only when Stage 1 flags a failure or repeated warnings. Compares earlier baseline + previous clean + current frames to rule out supports, purge towers, glossy filament reflections, or shadows before pausing. Can run cloud -> local reasoning fallback (or single-stage mode when Stage 2 is set to `none`).
- **Per-Layer Z Tracking (Toolchanger & Z-hop Safe):**
  - Ignores short Z-hops and large multi-tool travel lifts (`Z >ConfirmedZ + z_max_step_mm`).
  - Re-baselines cleanly downward if warmup ended while parked at high Z (e.g. `Z=40mm`) before Layer 1.
  - Falls back to a configurable `fallback_max_wait_sec` timer (default `60s`) for very long layers.
- **Flexible Camera Sources:**
  - **Prusa Connect Snapshots:** Reads stored frames natively via `core-storage.read` (`com.prusa.connect.camera.snapshot.captured`).
  - **Custom Live JPEG URL (`go2rtc` / HTTP):** Pulls live JPEGs directly from a configured URL (for example a single-connection `go2rtc` RTSP relay on `http://127.0.0.1:1984/api/frame.jpeg?src=coreone`), with automatic fallback to Prusa Connect snapshots.
- **Native UI Controls & Frame History:**
  - Both a fleet-wide **Printer Hawk** sidebar page and an embedded **Printer Hawk** tab on every printer page.
  - **Live Camera / Last AI Frame** toggle (`1s`, `2s`, `5s`, `Paused`).
  - **False Alarm (Accept as New Normal)** promotes the current bed frame as the new clean baseline and clears the pause latch.
  - Rolling historical frames saved on disk so you can click and inspect past checks.
- **Optional Event Bus & MQTT Alerts:**
  - Publishes `com.github.aux.ln4cy.printer-hawk.failure.detected` on Prusa Connect's event bus (works with Apprise, Pushover, etc.).
  - Optional lightweight pure-Go MQTT publisher for smart-home integrations such as Apple Home / Homebridge or Home Assistant.

## Configuration Combinations

Configure AI providers directly inside the **Printer Hawk -> AI Providers** tab (or via `module.yaml` / `resources.json` options):

| Setup | Stage 1 Primary | Stage 1 Fallback | Stage 2 Primary | Stage 2 Fallback |
| --- | --- | --- | --- | --- |
| **Hybrid LAN GPU + Cloud** | Ollama (`gemma4:26b`) | Gemini (`gemini-3.8-flash`) | Claude (`claude-sonnet-5-5`) | Ollama thinking (`gemma4:26b`) |
| **Local Only (Air-gapped)** | Ollama / LM Studio | Local VLM or `none` | Local reasoning VLM | `none` |
| **Cloud Only** | Gemini (`gemini-3.8-flash`) | OpenAI (`gpt-4o-mini`) | Claude (`claude-sonnet-5-5`) | `none` |
| **Single-Stage Fast** | Any VLM | Optional fallback | `none` | `none` |

## Operator Permissions for Auto-Pause

1. Approve the extension's requested `observe`, `read_storage`, and `operate` permissions during installation.
2. In Prusa Connect Local, grant `operate` permission for Printer Hawk (`sink:printer-hawk`) on any printer where you want **Action = Pause** to be able to send `core.command.pause_print`. Without that grant, Printer Hawk monitors, alerts, and logs incidents in **Notify** mode without pausing.

## Building from Source

```sh
make check    # Fetch SDK (.core/), generate/check OpenAPI client, run Go & Svelte checks
make release  # Build linux_amd64, linux_arm64, darwin_arm64, windows_amd64 + ui/dist
```
