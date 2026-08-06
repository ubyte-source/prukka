// Entry point: hand the provider to the CMIO extension runtime.

import CoreMediaIO
import Foundation

let providerSource = CameraProviderSource(clientQueue: nil)
CMIOExtensionProvider.startService(provider: providerSource.provider)
CFRunLoopRun()
