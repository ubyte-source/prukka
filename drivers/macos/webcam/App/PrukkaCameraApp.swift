// Prukka Camera host app: its only job is activating and deactivating the
// bundled camera extension (System Extensions require an owning app).

import SwiftUI
import SystemExtensions

let extensionIdentifier = "it.ubyte.prukka.camera.extension"

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
                    Button("Deactivate") { manager.deactivate() }
                }
            }
            .padding(32)
            .frame(width: 360)
        }
    }
}

final class ExtensionManager: NSObject, ObservableObject, OSSystemExtensionRequestDelegate {
    @Published var status = "Extension not activated."

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
