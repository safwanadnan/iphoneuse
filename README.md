# iPhoneUse

**Computer use, for your iPhone.**

Claude and ChatGPT can already use your computer. They can't use your phone. iPhoneUse gives any AI agent a pair of thumbs on a real iPhone: it can see the screen, tap, swipe, type and open apps, over a USB cable from your Mac.

```bash
brew install safwanadnan/tap/iphoneuse
iphoneuse setup                              # one time, about a minute
claude mcp add iphone -- iphoneuse mcp       # give Claude your phone
```

Then just ask:

> *"Open WhatsApp and tell Mom I'll be 10 minutes late."*
> *"Check my Uber Eats order status."*
> *"Turn on Low Power Mode."*

## What the agent sees

Each step, the agent gets a screenshot and a compact list of what's on screen. It acts by tapping a number, a label, or a point:

```
$ iphoneuse tree
app: com.apple.Preferences "Settings"  screen: 402x874 points
[1] NavBar "Settings" @(201,96) 402x52
[2] SearchField placeholder="Search" @(201,155) 370x36
[3] Cell "Wi-Fi" value="Home" @(201,318) 370x52
[4] Cell "Bluetooth" value="On" @(201,370) 370x52
…
$ iphoneuse tap --ref 4
tapped ref 4 at (201,370)
```

A screenshot takes about 0.2 s and the screen as text about 0.3 s, so the AI's thinking time is the only real wait.

## Why iPhoneUse

Android has had tools like this for years (`adb` and the agents built on it). On iPhone it has meant Appium servers, Xcode projects, WebDriverAgent builds that expire every 7 days, and a pile of setup steps. iPhoneUse puts all of that behind one command:

- **One install, one setup command.** `iphoneuse setup` checks Xcode, pairs the phone, waits for Developer Mode, picks your Apple ID, then builds, signs and installs the helper app on your phone.
- **Never expires.** With a free Apple ID, apps you install yourself stop working after 7 days. iPhoneUse quietly reinstalls a fresh copy before that, whenever the phone is connected.
- **Made for AI.** `iphoneuse mcp` works with Claude (Code and Desktop), ChatGPT and Codex, Cursor, and any other MCP client.
- **Works from the terminal too.** Scriptable, adb-style commands: `devices`, `-s <device>`, `tap`, `swipe`, `type`, `install`, `screencap`.
- **Simulators too.** Everything also works on iOS Simulators, with no Apple ID needed.

## Install

> **Mac only.** iPhoneUse runs on a Mac and controls your iPhone from it. Apple only allows this kind of control through Xcode, which only runs on macOS. No Mac, no iPhoneUse (for now).

One line, with Homebrew:

```bash
brew install safwanadnan/tap/iphoneuse
```

Or without Homebrew:

```bash
curl -fsSL https://raw.githubusercontent.com/safwanadnan/iphoneuse/main/install.sh | sh
```

Or from source (Go 1.22+):

```bash
go install github.com/safwanadnan/iphoneuse/cmd/iphoneuse@latest
```

### What you need

- **A Mac with Xcode** (free from the App Store). Open it once after installing.
- **An Apple ID signed in to Xcode** (Xcode → Settings → Accounts → +). A free Apple ID works.
- **An iPhone on iOS 17 or later** and a cable for the first setup. After that, Wi-Fi works too.

## Setup

Plug in your iPhone, unlock it, and run:

```bash
iphoneuse setup
```

It checks everything and tells you exactly what to tap. The first time, that's usually:

1. **Trust This Computer?** Tap Trust on the phone.
2. **Developer Mode.** Settings → Privacy & Security → Developer Mode → On. The phone restarts.
3. **Your Apple ID.** iPhoneUse uses the one signed in to Xcode.
4. **Trust the developer.** Settings → General → VPN & Device Management → your Apple ID → Trust.

Run `iphoneuse doctor` any time to see what's wrong.

### It keeps itself running

Setup installs a small **login service**: a background helper that starts when you log in to your Mac, like Dropbox or a menu bar app. It quietly:

- keeps the iPhone helper running whenever your phone is connected (cable or Wi-Fi),
- restarts it if it crashes or the phone reboots,
- **renews it before it expires.** With a free Apple ID, apps you install yourself stop working after 7 days. Two days before that, iPhoneUse fetches a fresh 7-day signature from Apple and reinstalls the helper, and you get a Mac notification. You never hit the deadline. With a paid developer account, signatures last a year.

You can renew by hand with `iphoneuse resign`, check it with `iphoneuse service status`, or remove it with `iphoneuse service uninstall`. Skip it entirely with `iphoneuse setup --no-service`; commands then start the helper on demand, but nothing renews it for you.

### Wireless

After setup by cable, unplug it: as long as the iPhone and Mac are on the same Wi-Fi and the phone is unlocked, everything keeps working. Commands take about 0.6 s over Wi-Fi versus under 0.1 s by cable.

## Commands

| Command | What it does |
|---|---|
| `iphoneuse devices` | List iPhones and booted simulators, and whether each is set up |
| `iphoneuse setup [--team ID] [--no-service]` | Set up the connected device |
| `iphoneuse doctor` | Check Xcode, signing teams, the service, devices and the helper |
| `iphoneuse screenshot [-o file] [--jpeg] [--scale 0.5\|points]` | Save a screenshot (`-o -` writes to stdout) |
| `iphoneuse tree [--json] [--app bundle.id]` | List on-screen elements with refs and coordinates |
| `iphoneuse tap <x> <y>` / `tap <text>` / `tap --ref N` | Tap. Add `--long` or `--double` |
| `iphoneuse swipe up\|down\|left\|right` / `swipe x1 y1 x2 y2` | Swipe. Direction is the finger's movement |
| `iphoneuse type <text> [--enter]` | Type into the focused field |
| `iphoneuse key <name>` | `home`, `lock`, `volume-up`, `volume-down`, `return`, `delete`, `tab`, `escape` |
| `iphoneuse wait <text> [--timeout 10] [--gone]` | Wait for text to appear (or disappear) |
| `iphoneuse app` | Show the foreground app |
| `iphoneuse launch <bundle-id> [--restart]` / `kill <bundle-id>` | Open or force-quit an app |
| `iphoneuse open <url>` | Open a URL or deep link (iOS 16.4+) |
| `iphoneuse apps` | List installed apps |
| `iphoneuse install <path>` / `uninstall <bundle-id>` | Install or remove an `.app` or `.ipa` |
| `iphoneuse mcp` / `iphoneuse mcp config` | Run the MCP server, or print config snippets |
| `iphoneuse service install\|uninstall\|status` | Manage the login service |
| `iphoneuse start-server` / `kill-server` | Start or stop the background daemon |
| `iphoneuse resign` | Renew the helper's signature now (normally automatic) |
| `iphoneuse logs [-n 50]` | Show the daemon log |
| `iphoneuse forget` | Remove a device's setup |

Pick a device with `-s <udid|name>` or `IPHONEUSE_DEVICE=…` when more than one is connected. Coordinates are in **points**. `iphoneuse screenshot --scale points` produces an image where pixel coordinates are tap coordinates.

## AI agents (MCP)

```bash
claude mcp add iphone -- iphoneuse mcp
```

Other clients:

```json
{ "mcpServers": { "iphone": { "command": "iphoneuse", "args": ["mcp"] } } }
```

Tools: `screen` (screenshot plus element list), `tree`, `tap`, `swipe`, `type_text`, `press_key`, `launch_app`, `terminate_app`, `foreground_app`, `list_apps`, `open_url`, `list_devices`.

The `screen` tool returns a JPEG at point resolution together with the numbered element list. The agent can tap by ref, by text, or by pixel coordinates read straight off the image.

## How it works

```
 your Mac                                   your iPhone
┌────────────────────────────┐  cable or   ┌─────────────────────────────┐
│ iphoneuse CLI / MCP server │──Wi-Fi────▶ │ iPhoneUse helper            │
│                            │  port 47100 │ (a UI-test app, Swift)      │
│ login service (launchd)    │             │  tap, swipe, type, read     │
│  · keeps the helper alive  │──Xcode────▶ │  the screen, open apps      │
│  · renews its signature    │             └─────────────────────────────┘
└────────────────────────────┘
```

- **Why a helper app on the phone?** iOS has no way for a computer to inject taps. Apple's only supported path is XCUITest, its UI-testing framework, which runs as a small test app on the phone. The helper is that app (Swift, in [`helper/`](helper/)); it serves a tiny HTTP API.
- **Why is it built on your Mac?** Apple doesn't allow handing out a pre-signed copy. The `iphoneuse` binary carries the helper's source code, and `setup` builds it with *your* Apple ID.
- **Why the login service?** A UI test only runs while Xcode's `xcodebuild` keeps it going. The service keeps one running per phone, restarts it if it dies, and renews the signature 48 hours before it expires.
- **How does it reach the phone?** By cable, through `usbmuxd` (the same service Finder uses). Over Wi-Fi, through the network tunnel Xcode opens to the phone. Simulators are reached on localhost.

## Limits (Apple's, not ours)

- **Mac only**: a Mac with Xcode is required, for signing and for running XCUITest.
- With a free Apple ID: 7-day signatures (handled automatically), at most 3 sideloaded apps per phone, and 10 new app IDs per week (iPhoneUse uses one).
- The phone must be unlocked when the helper starts. While automating, set Settings → Display & Brightness → Auto-Lock → Never.
- The helper can't unlock the phone or enter the passcode, and some system screens (passcode, Apple Pay, parts of Settings) refuse automation.
- Volume keys don't exist on simulators.

## Development

```bash
make build                 # bin/iphoneuse
make test
make helper-project        # regenerate helper/iPhoneUseHelper.xcodeproj from project.yml (needs xcodegen)
make helper-check          # compile the helper for the simulator
make release               # after tagging vX.Y.Z: publish binaries + Homebrew formula
```

The helper's Xcode project is generated from [`helper/project.yml`](helper/project.yml) and committed, so users never need XcodeGen.

## License

MIT. See [LICENSE](LICENSE). iPhoneUse is an independent open-source project, not affiliated with or endorsed by Apple. iPhone, iOS and Xcode are trademarks of Apple Inc.
