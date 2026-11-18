# iadb — adb for iPhone

Control a real iPhone from your Mac over a USB cable: tap, swipe, type, read the screen, launch apps. It works from your terminal, from scripts, and from AI agents over MCP.

```bash
iadb setup                 # one time: builds, signs and installs the helper on your phone
iadb screenshot
iadb tree                  # what's on screen, as text
iadb tap "Settings"
iadb swipe up
iadb type "hello"
```

```
$ iadb tree
app: com.apple.Preferences "Settings"  screen: 402x874 points
[1] NavBar "Settings" @(201,96) 402x52
[2] SearchField placeholder="Search" @(201,155) 370x36
[3] Cell "Wi-Fi" value="Home" @(201,318) 370x52
[4] Cell "Bluetooth" value="On" @(201,370) 370x52
…
$ iadb tap --ref 4
tapped ref 4 at (201,370)
```

## Why

Android has `adb`. iPhone automation has meant Appium servers, Xcode projects, WebDriverAgent builds that expire every 7 days, and a pile of setup steps. iadb puts all of that behind one command:

- **One binary, one setup command.** `iadb setup` checks Xcode, pairs the phone, waits for Developer Mode, picks your signing team, then builds, signs and installs the helper.
- **Never expires.** With a free Apple ID, iOS signatures last 7 days. iadb's login service re-signs the helper automatically before that happens, whenever the phone is connected.
- **Fast, compact screen reading.** `iadb tree` lists only the visible, meaningful elements, each with a ref number and tap coordinates, so it reads like a page instead of an XML dump. That makes it ideal for AI agents.
- **Built for AI agents.** `iadb mcp` is an MCP server. Claude, Cursor, Codex and other MCP clients can drive your phone.
- **adb-style commands.** `devices`, `-s <device>`, `install`, `uninstall`, `screencap`, `start-server`, `kill-server`.
- **Simulators too.** Everything also works on booted iOS Simulators, with no signing needed.

## Install

```bash
brew install iadb-dev/tap/iadb
```

Or from source (Go 1.22+):

```bash
go install github.com/iadb-dev/iadb/cmd/iadb@latest
```

### Requirements

- A Mac with **Xcode** installed (from the App Store; open it once, and accept the license with `sudo xcodebuild -license accept`).
- An **Apple ID** signed in to Xcode → Settings → Accounts. A free Apple ID works.
- An iPhone or iPad on **iOS 17 or later**, connected with a cable. Wi-Fi works too once Xcode's "Connect via network" is on.

## Setup

Plug in your iPhone, unlock it, and run:

```bash
iadb setup
```

iadb walks you through anything that's missing:

1. **Trust This Computer?** Tap Trust on the phone.
2. **Developer Mode.** Settings → Privacy & Security → Developer Mode → On. The phone restarts.
3. **Signing.** iadb uses your Apple ID's team (a paid team is preferred if you have one).
4. **Trust the developer** (first time with a free Apple ID): Settings → General → VPN & Device Management → your Apple ID → Trust.

When it finishes, it installs a login service that keeps the helper running and re-signs it before it expires. You can skip that with `--no-service`.

Run `iadb doctor` any time to see what's wrong.

## Commands

| Command | What it does |
|---|---|
| `iadb devices` | List iPhones and booted simulators, and whether each is set up |
| `iadb setup [--team ID] [--no-service]` | Set up the connected device |
| `iadb doctor` | Check Xcode, signing teams, the service, devices and the helper |
| `iadb screenshot [-o file] [--jpeg] [--scale 0.5\|points]` | Save a screenshot (`-o -` writes to stdout) |
| `iadb tree [--json] [--app bundle.id]` | List on-screen elements with refs and coordinates |
| `iadb tap <x> <y>` / `tap <text>` / `tap --ref N` | Tap. Add `--long` or `--double` |
| `iadb swipe up\|down\|left\|right` / `swipe x1 y1 x2 y2` | Swipe. Direction is the finger's movement |
| `iadb type <text> [--enter]` | Type into the focused field |
| `iadb key <name>` | `home`, `lock`, `volume-up`, `volume-down`, `return`, `delete`, `tab`, `escape` |
| `iadb wait <text> [--timeout 10] [--gone]` | Wait for text to appear (or disappear) |
| `iadb app` | Show the foreground app |
| `iadb launch <bundle-id> [--restart]` / `kill <bundle-id>` | Open or force-quit an app |
| `iadb open <url>` | Open a URL or deep link (iOS 16.4+) |
| `iadb apps` | List installed apps |
| `iadb install <path>` / `uninstall <bundle-id>` | Install or remove an `.app` or `.ipa` |
| `iadb mcp` / `iadb mcp config` | Run the MCP server, or print config snippets |
| `iadb service install\|uninstall\|status` | Manage the login service |
| `iadb start-server` / `kill-server` | Start or stop the background daemon |
| `iadb logs [-n 50]` | Show the daemon log |
| `iadb forget` | Remove a device's setup |

Pick a device with `-s <udid|name>` or `IADB_DEVICE=…` when more than one is connected. Coordinates are in **points**. `iadb screenshot --scale points` produces an image where pixel coordinates are tap coordinates.

## AI agents (MCP)

```bash
claude mcp add iadb -- iadb mcp
```

Other clients:

```json
{ "mcpServers": { "iadb": { "command": "iadb", "args": ["mcp"] } } }
```

Tools: `screen` (screenshot plus element list), `tree`, `tap`, `swipe`, `type_text`, `press_key`, `launch_app`, `terminate_app`, `foreground_app`, `list_apps`, `open_url`, `list_devices`.

The `screen` tool returns a JPEG at point resolution together with the numbered element list. The agent can tap by ref, by text, or by pixel coordinates read straight off the image.

## How it works

```
 your Mac                                         your iPhone
┌──────────────────────────┐   USB (usbmuxd)    ┌──────────────────────────────┐
│ iadb CLI / MCP server    │ ─────────────────▶ │ IADBRunner (XCUITest bundle) │
│   └ HTTP over usbmux     │      port 47100    │   HTTP server → XCTest APIs: │
│ iadb daemon (launchd)    │                    │   tap, swipe, type, snapshot,│
│   └ xcodebuild test-     │ ─ keeps alive ───▶ │   screenshot, launch apps    │
│     without-building     │                    └──────────────────────────────┘
│   └ re-signs before      │
│     the 7-day expiry     │
└──────────────────────────┘
```

- **Why a helper app?** iOS has no way to inject input from a computer. Apple's only supported path is XCUITest, a UI-test bundle running on the device. The helper is a small XCUITest runner (Swift, in [`helper/`](helper/)) that serves an HTTP API.
- **Why does it have to be built on your Mac?** Apple doesn't allow distributing a pre-signed helper. The iadb binary embeds the helper's source, and `setup` builds it with *your* Apple ID, under a bundle ID unique to your team.
- **Why the daemon?** Running an XCUITest needs a live `xcodebuild` process. The daemon keeps one per device, restarts it if it dies, and re-signs the helper 48 hours before its profile expires.
- **How does the CLI reach the phone?** Directly through `usbmuxd`, the same service Finder and Xcode use, so no port forwarding is needed. Simulators are reached on localhost.

## Limits (Apple's, not ours)

- A Mac with Xcode is required, for signing and for running XCUITest.
- With a free Apple ID: 7-day signatures (handled automatically), at most 3 sideloaded apps per phone, and 10 new app IDs per week (iadb uses one).
- The helper can't unlock the phone or enter the passcode, and some system screens (passcode, Apple Pay, parts of Settings) refuse automation.
- `open <url>` needs iOS 16.4+. Volume keys don't exist on simulators.

## Development

```bash
make build                 # bin/iadb
make test
make helper-project        # regenerate helper/IADBHelper.xcodeproj from project.yml (needs xcodegen)
make helper-check          # compile the helper for the simulator
```

The helper's Xcode project is generated from [`helper/project.yml`](helper/project.yml) and committed, so users never need XcodeGen.

## License

MIT. See [LICENSE](LICENSE). iadb is not affiliated with Apple. iPhone, iOS and Xcode are trademarks of Apple Inc.
