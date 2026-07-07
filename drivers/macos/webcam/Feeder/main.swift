// prukka-camfeed <hls-url> — plays a Prukka session's video (dubbed audio
// and burned captions included when the URL says so) and pushes its frames
// into the Prukka Camera sink stream, so any video app sees the translated
// feed as a normal webcam.

import AVFoundation
import CoreMediaIO
import Foundation

let feedDeviceUID = "PrukkaCameraDeviceUID"
let feedFPS: Int32 = 30

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

// findSink locates the Prukka camera and its sink stream (the second
// stream the extension registers).
func findSink() -> (device: CMIODeviceID, stream: CMIOStreamID)? {
    for device in deviceIDs() {
        let uid = stringProperty(
            of: device, CMIOObjectPropertySelector(kCMIODevicePropertyDeviceUID))

        guard uid == feedDeviceUID else { continue }

        let streams = streamIDs(of: device)
        guard streams.count >= 2 else { return nil }

        return (device, streams[1])
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

    func enqueue(_ pixelBuffer: CVPixelBuffer) {
        guard let queue, CMSimpleQueueGetCount(queue) < CMSimpleQueueGetCapacity(queue)
        else { return }

        var description: CMFormatDescription?
        CMVideoFormatDescriptionCreateForImageBuffer(
            allocator: kCFAllocatorDefault, imageBuffer: pixelBuffer,
            formatDescriptionOut: &description)

        guard let description else { return }

        var timing = CMSampleTimingInfo(
            duration: CMTime(value: 1, timescale: feedFPS),
            presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()),
            decodeTimeStamp: .invalid)

        var sample: CMSampleBuffer?
        CMSampleBufferCreateReadyWithImageBuffer(
            allocator: kCFAllocatorDefault, imageBuffer: pixelBuffer,
            formatDescription: description, sampleTiming: &timing,
            sampleBufferOut: &sample)

        if let sample {
            CMSimpleQueueEnqueue(queue, element: Unmanaged.passRetained(sample).toOpaque())
        }
    }

    deinit {
        CMIODeviceStopStream(device, stream)
    }
}

// MARK: - Player

guard CommandLine.arguments.count == 2, let url = URL(string: CommandLine.arguments[1]) else {
    FileHandle.standardError.write(Data("usage: prukka-camfeed <hls-url>\n".utf8))
    exit(2)
}

// Third-party camera extensions are hidden from their own host by default;
// this opts the DAL enumeration in.
var allow: UInt32 = 1
var allowAddress = objectProperty(
    CMIOObjectPropertySelector(kCMIOHardwarePropertyAllowScreenCaptureDevices))
CMIOObjectSetPropertyData(
    CMIOObjectID(kCMIOObjectSystemObject), &allowAddress, 0, nil,
    UInt32(MemoryLayout<UInt32>.size), &allow)

guard let found = findSink(), let sink = Sink(device: found.device, stream: found.stream) else {
    FileHandle.standardError.write(
        Data("Prukka Camera not found — activate it from the Prukka Camera app first\n".utf8))
    exit(1)
}

let player = AVPlayer(url: url)
let output = AVPlayerItemVideoOutput(pixelBufferAttributes: [
    kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA
])

player.currentItem?.add(output)
player.play()

FileHandle.standardOutput.write(Data("feeding \(url) → Prukka Camera\n".utf8))

let timer = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "camfeed"))
timer.schedule(deadline: .now(), repeating: 1.0 / Double(feedFPS))
timer.setEventHandler {
    let now = output.itemTime(forHostTime: CACurrentMediaTime())

    guard output.hasNewPixelBuffer(forItemTime: now),
        let pixelBuffer = output.copyPixelBuffer(forItemTime: now, itemTimeForDisplay: nil)
    else { return }

    sink.enqueue(pixelBuffer)
}
timer.resume()

RunLoop.main.run()
