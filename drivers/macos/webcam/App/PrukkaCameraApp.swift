// Prukka Camera host app: its only job is activating and deactivating the
// bundled camera extension (System Extensions require an owning app).

import Security
import SwiftUI
import SystemExtensions

let extensionIdentifier = "it.ubyte.prukka.camera.extension"

// entitled reports whether this build carries the system-extension
// entitlement. An ad-hoc build cannot (AMFI would kill it at launch), so
// activation is honestly not offered instead of failing obscurely.
func entitled() -> Bool {
    guard let task = SecTaskCreateFromSelf(nil) else { return false }

    let value = SecTaskCopyValueForEntitlement(
        task, "com.apple.developer.system-extension.install" as CFString, nil)

    return (value as? Bool) == true
}

@main
struct PrukkaCameraApp: App {
    @StateObject private var manager = ExtensionManager()

    var body: some Scene {
        WindowGroup("Prukka Camera") {
            VStack(spacing: 16) {
                Text("Prukka Camera").font(.title2).bold()
                Text(manager.status).foregroundColor(.secondary)

                HStack(spacing: 12) {
                    Button("Activate") { manager.activate() }
                        .disabled(!manager.signed)
                    Button("Deactivate") { manager.deactivate() }
                        .disabled(!manager.signed)
                }
            }
            .padding(32)
            .frame(width: 360)
        }
    }
}

final class ExtensionManager: NSObject, ObservableObject, OSSystemExtensionRequestDelegate {
    let signed = entitled()

    @Published var status = "Extension not activated."

    override init() {
        super.init()

        if !signed {
            status = "This build is not Developer-ID signed, so macOS cannot "
                + "activate the camera extension yet. The Prukka microphone "
                + "and speaker are fully functional."
        }
    }

    func activate() {
        let request = OSSystemExtensionRequest.activationRequest(
            forExtensionWithIdentifier: extensionIdentifier, queue: .main)
        request.delegate = self
        OSSystemExtensionManager.shared.submitRequest(request)
        status = "Activation requested — approve it in System Settings if asked."
    }

    func deactivate() {
        let request = OSSystemExtensionRequest.deactivationRequest(
            forExtensionWithIdentifier: extensionIdentifier, queue: .main)
        request.delegate = self
        OSSystemExtensionManager.shared.submitRequest(request)
        status = "Deactivation requested."
    }

    func request(
        _ request: OSSystemExtensionRequest,
        actionForReplacingExtension existing: OSSystemExtensionProperties,
        withExtension ext: OSSystemExtensionProperties
    ) -> OSSystemExtensionRequest.ReplacementAction {
        .replace
    }

    func requestNeedsUserApproval(_ request: OSSystemExtensionRequest) {
        DispatchQueue.main.async {
            self.status = "Approve the extension in System Settings → Privacy & Security."
        }
    }

    func request(
        _ request: OSSystemExtensionRequest,
        didFinishWithResult result: OSSystemExtensionRequest.Result
    ) {
        DispatchQueue.main.async {
            self.status =
                result == .completed
                ? "Prukka Camera is active — select it in any video app."
                : "Finished: \(result)."
        }
    }

    func request(_ request: OSSystemExtensionRequest, didFailWithError error: Error) {
        DispatchQueue.main.async {
            self.status = "Failed: \(error.localizedDescription)"
        }
    }
}
