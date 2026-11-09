# BRIEF — etunnel hero background loop

**Deliverable**: seamless-loop hero background video for the etunnel homepage (https://t.etun.uk).

## Spec
- Exactly 10.000 s, 1920x1080, 30 fps, no audio, 300 frames.
- Frame 0 and frame 299 visually identical (true seamless loop): all animation is a pure
  function of time with loop period `LOOP = 299/30 s`; state(299/30) === state(0).
- Outputs (to `/Volumes/new/ggai/et/assets/`):
  - `hero-loop.webm` — VP9, < 2 MB (primary)
  - `hero-loop.mp4`  — H.264, < 3 MB (fallback)

## Creative (tunnel data-flow metaphor)
- Brand: bg `#0d1117`, fg `#e6edf3`, accent `#3fb950`. Monospace (JetBrains Mono, SF Mono vibe).
- Left: small terminal window `localhost:3000` (etunnel CLI, blinking cursor).
- Right: browser address-bar pill `https://<subdomain>.t.etun.uk`.
- Middle: glowing tunnel channel; green packet capsules with faint trails flow left -> right,
  slight acceleration + glow pulse mid-tunnel. Small secondary streaks for continuous flow.
- Background: ultra-faint grid; occasional vertical light sweep.
- Beat (3x per loop, P = LOOP/3): packet arrives -> address bar flashes green -> subdomain
  crossfades (quiet-frog-42 -> amber-otter-97 -> crimson-lynx-23 -> back to quiet-frog-42) ->
  new packet departs the terminal.
- Restrained: no strobing, no 3D, no particle explosions. Professional dev-tool background,
  low-contrast text.

## Implementation
- HyperFrames project (this dir). Single canvas scene driven by the `hf-seek` protocol
  (deterministic pure function of t; no Date.now, no unseeded random, no timers).
- Verify loop before render via snapshots at t=0 and t=299/30 (must match).
