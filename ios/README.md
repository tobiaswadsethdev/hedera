# Hedera for iOS

SwiftUI app plus `HederaKit`, the Swift package with the protocol, pinning,
Keychain storage and reconnect logic. The Xcode project is generated with
[XcodeGen](https://github.com/yonaskolb/XcodeGen) from `project.yml`; the
`.xcodeproj` is not checked in.

## Build

Needs Xcode 16 or newer and XcodeGen (`brew install xcodegen`). SwiftTerm
compiles a Metal shader, so newer Xcodes also need the Metal toolchain
(`xcodebuild -downloadComponent MetalToolchain`, once).

```sh
cd ios
xcodegen
open Hedera.xcodeproj         # or build and test from the command line:
xcodebuild -project Hedera.xcodeproj -scheme Hedera \
  -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation test
cd HederaKit && swift test    # HederaKit alone, on the Mac
```

The bundle ID (`com.sprigbyte.hedera`) and team are set in
`Config/Hedera.xcconfig`. To sign under another team, override them in
`Config/Signing.local.xcconfig`, which git ignores:

```
HEDERA_BUNDLE_ID = com.yourname.hedera
HEDERA_TEAM_ID = ABCDE12345
```

## Notifications

Notifications go through Radix, the push relay in `relay/`. Deploy it first
(`relay/README.md`), then set its host in `Config/Hedera.xcconfig`:

```
HEDERA_RADIX_HOST = radix.example.workers.dev
```

With it empty, the app builds without the Notifications section. Turn
notifications on per machine on the machine's screen. Builds run from Xcode
register as `sandbox` with APNs and TestFlight builds as `production`; Radix
needs to know which, and the app tells it.

To try the app's side without Radix, send a payload straight to the
simulator, with `machine_id` and `session_id` from a paired machine:

```sh
xcrun simctl push booted com.sprigbyte.hedera payload.json
```

## Try it in the simulator

The simulator reaches the Mac at 127.0.0.1, so run an agent with the remote
API on loopback and open its pairing link in the simulator:

```sh
hedera run --dev-loopback &
hedera pair --no-qr                                   # waits; copy the hedera://pair… URL
xcrun simctl openurl booted 'hedera://pair?…'         # or paste it on the Pair screen
```

On a phone, scan the QR code from `hedera pair` with the Pair screen or the
Camera app; the phone needs Tailscale connected. If your simulator has a
passcode but no enrolled Face ID, use Features → Face ID → Enrolled, then
Matching Face to unlock.

`HederaKit/Tests/HederaKitTests/LiveAgentTests.swift` runs the same flow
against a real agent from `swift test`; see the comment at its top.

## Layout

| Path | What |
|---|---|
| `HederaKit/Sources/HederaKit/Protocol` | Codable messages and the binary frame codec |
| `HederaKit/Sources/HederaKit/Net` | SPKI pinning, pinned TLS and HTTP on Network.framework, the WebSocket transport |
| `HederaKit/Sources/HederaKit/Pairing` | `hedera://pair` parsing and `POST /v1/pair` |
| `HederaKit/Sources/HederaKit/Push` | Radix client, device registration, linking machines for notifications |
| `HederaKit/Sources/HederaKit/Storage` | Keychain storage of paired machines |
| `HederaKit/Sources/HederaKit/Client` | `AgentConnection` (one socket) and `MachineClient` (reconnecting, sessions, terminals) |
| `Hedera/` | the app: lock, machine list, pairing, sessions, SwiftTerm terminal |
| `Tools/make-icon.swift` | draws the app icon |
