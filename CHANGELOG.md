# Changelog

## 0.1.0 - 2026-11-25

- Initial release of Printer Hawk native Prusa Connect Local extension (`github.com/LN4CY/printer-hawk`).
- Dual-stage configurable VLM pipeline (Ollama, Gemini, Anthropic Claude, OpenAI-compatible) with automatic per-stage primary -> fallback failover.
- Per-printer LayerTracker with Z-hop/toolchange filtering, heat-up park downward re-baselining, and long-layer fallback timer.
- Support for both native Prusa Connect snapshots (`storageapi`) and direct HTTP live JPEG feeds (such as `go2rtc`).
- Fleet sidebar page, per-printer `Printer Hawk` tab, live feed rate switcher, clickable check frame history, False Alarm baseline reset, `failure.detected` event emission, and optional MQTT alerts.
