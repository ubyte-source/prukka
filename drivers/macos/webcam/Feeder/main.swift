// prukka-camfeed <hls-url> — plays a Prukka session's video and pushes
// fixed-format frames into the Prukka Camera sink stream.

import AVFoundation
import CoreImage
import CoreMediaIO
import Foundation

let feedDeviceUID = "PrukkaCameraDeviceUID"
let feedFPS: Int32 = 30
let feedWidth = 1280
let feedHeight = 720

// MARK: - DAL sink plumbing

func objectProperty(_ selector: CMIOObjectPropertySelector) -> CMIOObjectPropertyAddress {
    CMIOObjectPropertyAddress(
        mSelector: selector,
        mScope: CMIOObjectPropertyScope(kCMIOObjectPropertyScopeGlobal),
        mElement: CMIOObjectPropertyElement(kCMIOObjectPropertyElementMain))
}

func deviceIDs() -> [CMIODeviceID] {
    var address = objectProperty(CMIOObjectPropertySelector(kCMIOHardwarePropertyDevices))
    var byteSize: UInt32 = 0

    guard CMIOObjectGetPropertyDataSize(
        CMIOObjectID(kCMIOObjectSystemObject), &address, 0, nil, &byteSize) == 0,
        byteSize > 0
    else { return [] }

    let count = Int(byteSize) / MemoryLayout<CMIODeviceID>.size
    var ids = [CMIODeviceID](repeating: 0, count: count)
    var used: UInt32 = 0

    guard CMIOObjectGetPropertyData(
        CMIOObjectID(kCMIOObjectSystemObject), &address, 0, nil, byteSize, &used, &ids) == 0
    else { return [] }

    return ids
}

func stringProperty(of object: CMIOObjectID, _ selector: CMIOObjectPropertySelector) -> String? {
    var address = objectProperty(selector)
    var value: CFString? = nil
    var used: UInt32 = 0
    let size = UInt32(MemoryLayout<CFString?>.size)

    let status = withUnsafeMutablePointer(to: &value) { pointer in
        CMIOObjectGetPropertyData(object, &address, 0, nil, size, &used, pointer)
    }

    guard status == 0, let value else { return nil }

    return value as String
}

func streamIDs(of device: CMIODeviceID) -> [CMIOStreamID] {
    var address = objectProperty(CMIOObjectPropertySelector(kCMIODevicePropertyStreams))
    var byteSize: UInt32 = 0

    guard CMIOObjectGetPropertyDataSize(device, &address, 0, nil, &byteSize) == 0, byteSize > 0
    else { return [] }

    let count = Int(byteSize) / MemoryLayout<CMIOStreamID>.size
    var ids = [CMIOStreamID](repeating: 0, count: count)
    var used: UInt32 = 0

    guard CMIOObjectGetPropertyData(device, &address, 0, nil, byteSize, &used, &ids) == 0
    else { return [] }

    return ids
}

// findSink locates the Prukka camera and its sink stream — matched by the
// name the extension gives it, not by position in the stream list.
func findSink() -> (device: CMIODeviceID, stream: CMIOStreamID)? {
    for device in deviceIDs() {
        let uid = stringProperty(
            of: device, CMIOObjectPropertySelector(kCMIODevicePropertyDeviceUID))

        guard uid == feedDeviceUID else { continue }

        for stream in streamIDs(of: device) {
            let name = stringProperty(
                of: stream, CMIOObjectPropertySelector(kCMIOObjectPropertyName))

            if name?.hasSuffix("Sink") == true {
                return (device, stream)
            }
        }

        return nil
    }

    return nil
}

// Sink wraps the stream's buffer queue for enqueuing frames.
final class Sink {
    private let device: CMIODeviceID
    private let stream: CMIOStreamID
    private var queue: CMSimpleQueue?

    init?(device: CMIODeviceID, stream: CMIOStreamID) {
        self.device = device
        self.stream = stream

        var queueRef: Unmanaged<CMSimpleQueue>?
        let status = CMIOStreamCopyBufferQueue(stream, { _, _, _ in }, nil, &queueRef)

        guard status == 0, let queueRef else { return nil }

        queue = queueRef.takeRetainedValue()

        guard CMIODeviceStartStream(device, stream) == 0 else { return nil }
    }

    func enqueue(_ pixelBuffer: CVPixelBuffer) -> Bool {
        guard let queue, CMSimpleQueueGetCount(queue) < CMSimpleQueueGetCapacity(queue)
        else { return false }

        var description: CMFormatDescription?
        CMVideoFormatDescriptionCreateForImageBuffer(
            allocator: kCFAllocatorDefault, imageBuffer: pixelBuffer,
            formatDescriptionOut: &description)

        guard let description else { return false }

        var timing = CMSampleTimingInfo(
            duration: CMTime(value: 1, timescale: feedFPS),
            presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()),
            decodeTimeStamp: .invalid)

        var sample: CMSampleBuffer?
        CMSampleBufferCreateReadyWithImageBuffer(
            allocator: kCFAllocatorDefault, imageBuffer: pixelBuffer,
            formatDescription: description, sampleTiming: &timing,
            sampleBufferOut: &sample)

        guard let sample else { return false }

        let retained = Unmanaged.passRetained(sample)
        guard CMSimpleQueueEnqueue(queue, element: retained.toOpaque()) == noErr else {
            retained.release()

            return false
        }

        return true
    }

    deinit {
        CMIODeviceStopStream(device, stream)
    }
}

final class FrameScaler {
    private let bounds = CGRect(x: 0, y: 0, width: feedWidth, height: feedHeight)
    private let colorSpace = CGColorSpaceCreateDeviceRGB()
    private let context = CIContext(options: [.cacheIntermediates: false])
    private let pool: CVPixelBufferPool

    init?() {
        let attributes: [String: Any] = [
            kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA,
            kCVPixelBufferWidthKey as String: feedWidth,
            kCVPixelBufferHeightKey as String: feedHeight,
            kCVPixelBufferIOSurfacePropertiesKey as String: [String: Any](),
        ]
        var created: CVPixelBufferPool?
        guard CVPixelBufferPoolCreate(
            kCFAllocatorDefault, nil, attributes as CFDictionary, &created) == kCVReturnSuccess,
            let created
        else { return nil }

        pool = created
    }

    func render(_ source: CVPixelBuffer) -> CVPixelBuffer? {
        var destination: CVPixelBuffer?
        guard CVPixelBufferPoolCreatePixelBuffer(
            kCFAllocatorDefault, pool, &destination) == kCVReturnSuccess,
            let destination
        else { return nil }

        let image = CIImage(cvPixelBuffer: source)
        guard image.extent.width > 0, image.extent.height > 0 else { return nil }

        let scale = min(bounds.width / image.extent.width, bounds.height / image.extent.height)
        let scaled = image.transformed(by: CGAffineTransform(scaleX: scale, y: scale))
        let translated = scaled.transformed(by: CGAffineTransform(
            translationX: (bounds.width - scaled.extent.width) / 2 - scaled.extent.minX,
            y: (bounds.height - scaled.extent.height) / 2 - scaled.extent.minY))
        let background = CIImage(color: CIColor(red: 0, green: 0, blue: 0)).cropped(to: bounds)

        context.render(
            translated.composited(over: background), to: destination,
            bounds: bounds, colorSpace: colorSpace)

        return destination
    }
}

// MARK: - Player

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(
        Data("usage: prukka-camfeed <hls-url|--probe|--self-test>\n".utf8))
    exit(2)
}

let source = CommandLine.arguments[1]

if source == "--self-test" {
    var input: CVPixelBuffer?
    let attributes: [String: Any] = [
        kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA,
    ]
    guard CVPixelBufferCreate(
        kCFAllocatorDefault, 640, 480, kCVPixelFormatType_32BGRA,
        attributes as CFDictionary, &input) == kCVReturnSuccess,
        let input,
        let output = FrameScaler()?.render(input),
        CVPixelBufferGetWidth(output) == feedWidth,
        CVPixelBufferGetHeight(output) == feedHeight,
        CVPixelBufferGetPixelFormatType(output) == kCVPixelFormatType_32BGRA
    else {
        FileHandle.standardError.write(Data("frame scaler self-test failed\n".utf8))
        exit(1)
    }

    exit(0)
}

// Camera-extension devices are hidden from the legacy DAL enumeration
// this tool uses until the process opts into virtual capture devices.
var allow: UInt32 = 1
var allowAddress = objectProperty(
    CMIOObjectPropertySelector(kCMIOHardwarePropertyAllowScreenCaptureDevices))
CMIOObjectSetPropertyData(
    CMIOObjectID(kCMIOObjectSystemObject), &allowAddress, 0, nil,
    UInt32(MemoryLayout<UInt32>.size), &allow)

guard let found = findSink() else {
    FileHandle.standardError.write(
        Data("Prukka Camera not found — activate it from the Prukka Camera app first\n".utf8))
    exit(1)
}

if source == "--probe" {
    exit(0)
}

let url: URL
if source.contains("://") {
    guard let parsed = URL(string: source) else {
        FileHandle.standardError.write(Data("invalid HLS URL: \(source)\n".utf8))
        exit(2)
    }
    url = parsed
} else {
    url = URL(fileURLWithPath: source)
}

guard let sink = Sink(device: found.device, stream: found.stream) else {
    FileHandle.standardError.write(Data("Prukka Camera sink could not start\n".utf8))
    exit(1)
}

guard let scaler = FrameScaler() else {
    FileHandle.standardError.write(Data("Prukka Camera frame scaler could not start\n".utf8))
    exit(1)
}

let player = AVPlayer(url: url)
let output = AVPlayerItemVideoOutput(pixelBufferAttributes: [
    kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA
])

player.currentItem?.add(output)
player.play()

let timer = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "camfeed"))
timer.schedule(deadline: .now(), repeating: 1.0 / Double(feedFPS))
var signalledReady = false
timer.setEventHandler {
    let now = output.itemTime(forHostTime: CACurrentMediaTime())

    guard output.hasNewPixelBuffer(forItemTime: now),
        let pixelBuffer = output.copyPixelBuffer(forItemTime: now, itemTimeForDisplay: nil),
        let frame = scaler.render(pixelBuffer)
    else { return }

    if sink.enqueue(frame), !signalledReady {
        signalledReady = true
        FileHandle.standardOutput.write(Data("ready\n".utf8))
    }
}
timer.resume()

RunLoop.main.run()
