from __future__ import annotations

import asyncio
import json
import urllib.error
import urllib.parse
import urllib.request

from support import Port, record_tool_call

CAP = "weather.fetch"
URL = "https://api.open-meteo.com/v1/forecast"
TIMEOUT_S = 10.0
CITIES = {"paris": (48.8566, 2.3522), "lyon": (45.7640, 4.8357), "marseille": (43.2965, 5.3698), "lille": (50.6292, 3.0573)}


def _get(url: str) -> dict:
    with urllib.request.urlopen(url, timeout=TIMEOUT_S) as response:
        return json.loads(response.read().decode("utf-8"))


# <brique:capacity name="weather.fetch">
async def weather_fetch(runtime, params: dict) -> dict:
    port = Port(runtime, CAP)
    city = str(params.get("city", "")).strip().lower()
    if not city:
        raise ValueError("city is required")
    if city not in CITIES:
        raise LookupError(f"city {city} is not known (known: {', '.join(sorted(CITIES))})")
    latitude, longitude = CITIES[city]
    query = urllib.parse.urlencode({"latitude": latitude, "longitude": longitude, "timezone": "auto",
                                    "current": "temperature_2m,apparent_temperature,precipitation,wind_speed_10m"})
    try:
        body = await asyncio.get_running_loop().run_in_executor(None, _get, f"{URL}?{query}")
    except (urllib.error.URLError, OSError, ValueError) as exc:
        await record_tool_call(port, CAP, params, "failed")
        raise RuntimeError(f"weather service unreachable: {exc}") from exc
    current = body.get("current") or {}
    await record_tool_call(port, CAP, params, "done")
    return {"city": city, "observed_at": current.get("time"), "temperature_c": current.get("temperature_2m"),
            "feels_like_c": current.get("apparent_temperature"), "precipitation_mm": current.get("precipitation"),
            "wind_kmh": current.get("wind_speed_10m")}
# </brique:capacity>
