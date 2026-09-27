# Red-XAI iOS

Native Swift/SwiftUI targets for **Red-XAI Database** and **Red-XAI Host**.

## Current scope
- Database: custom .Red-XAI document editing and validation foundation.
- Host: native controller dashboard and node-enrollment foundation.
- Shared core: validation and visual tokens.
- CI: XcodeGen + Xcode simulator builds and XCTest.

## Distribution target
Two independent App Store Connect/TestFlight products:
- Red-XAI Database — bundle ID com.redxai.database
- Red-XAI Host — bundle ID com.redxai.host

The iPhone Host is a controller/opportunistic node. It is not treated as an unrestricted 24/7 server because iOS controls background execution.

No production credentials belong in this repository. Signing and App Store Connect credentials must be supplied through encrypted CI secrets.
