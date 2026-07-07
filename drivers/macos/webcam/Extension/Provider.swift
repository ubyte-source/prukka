// Prukka Camera — CoreMedia I/O camera extension.
//
// One virtual device with two streams: a sink the feeder writes dubbed
// video into, and a source every app reads as a normal camera. While no
// feeder is connected the source serves a generated splash frame, so the
// camera is always selectable.

import CoreMediaIO
import Foundation
import IOKit.audio
import os.log

let cameraName = "Prukka Camera"
let deviceUID = "PrukkaCameraDeviceUID"

let frameWidth = 1280
let frameHeight = 720
let framesPerSecond = 30

let log = OSLog(subsystem: "it.ubyte.prukka.camera", category: "extension")

// MARK: - Provider

class CameraProviderSource: NSObject, CMIOExtensionProviderSource {
    private(set) var provider: CMIOExtensionProvider!
    private var deviceSource: CameraDeviceSource!

    init(clientQueue: DispatchQueue?) {
        super.init()

        provider = CMIOExtensionProvider(source: self, clientQueue: clientQueue)
        deviceSource = CameraDeviceSource(localizedName: cameraName)

        do {
            try provider.addDevice(deviceSource.device)
        } catch {
            os_log(.error, log: log, "add device: %{public}@", error.localizedDescription)
        }
    }

    func connect(to client: CMIOExtensionClient) throws {}

    func disconnect(from client: CMIOExtensionClient) {}

    var availableProperties: Set<CMIOExtensionProperty> {
        [.providerManufacturer, .providerName]
    }

    func providerProperties(
        forProperties properties: Set<CMIOExtensionProperty>
    ) throws -> CMIOExtensionProviderProperties {
        let providerProperties = CMIOExtensionProviderProperties(dictionary: [:])

        if properties.contains(.providerName) {
            providerProperties.name = cameraName
        }

        if properties.contains(.providerManufacturer) {
            providerProperties.manufacturer = "Prukka"
        }

        return providerProperties
    }

    func setProviderProperties(_ providerProperties: CMIOExtensionProviderProperties) throws {}
}

// MARK: - Device

class CameraDeviceSource: NSObject, CMIOExtensionDeviceSource {
    private(set) var device: CMIOExtensionDevice!

    private var sourceStream: SourceStreamSource!
    private var sinkStream: SinkStreamSource!

    private var bufferPool: CVPixelBufferPool!
    private var formatDescription: CMFormatDescription!

    // The splash timer runs whenever an app streams but no feeder does.
    private var timer: DispatchSourceTimer?
    private let timerQueue = DispatchQueue(
        label: "it.ubyte.prukka.camera.timer", qos: .userInteractive)

    // Last frame written by the feeder; served in place of the splash.
    private let frameLock = NSLock()
    private var latestFrame: CVPixelBuffer?

    init(localizedName: String) {
        super.init()

        device = CMIOExtensionDevice(
            localizedName: localizedName, deviceID: UUID(),
            legacyDeviceID: deviceUID, source: self)

        var description: CMFormatDescription?
        CMVideoFormatDescriptionCreate(
            allocator: kCFAllocatorDefault,
            codecType: kCVPixelFormatType_32BGRA,
            width: Int32(frameWidth), height: Int32(frameHeight),
            extensions: nil, formatDescriptionOut: &description)
        formatDescription = description

        let poolAttributes: [String: Any] = [
            kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA,
            kCVPixelBufferWidthKey as String: frameWidth,
            kCVPixelBufferHeightKey as String: frameHeight,
            kCVPixelBufferIOSurfacePropertiesKey as String: [String: Any](),
        ]
        CVPixelBufferPoolCreate(
            kCFAllocatorDefault, nil, poolAttributes as CFDictionary, &bufferPool)

        let format = CMIOExtensionStreamFormat(
            formatDescription: formatDescription,
            maxFrameDuration: CMTime(value: 1, timescale: Int32(framesPerSecond)),
            minFrameDuration: CMTime(value: 1, timescale: Int32(framesPerSecond)),
            validFrameDurations: nil)

        sourceStream = SourceStreamSource(
            localizedName: "\(localizedName) Source", streamID: UUID(),
            streamFormat: format, device: self)
        sinkStream = SinkStreamSource(
            localizedName: "\(localizedName) Sink", streamID: UUID(),
            streamFormat: format, device: self)

        do {
            try device.addStream(sourceStream.stream)
            try device.addStream(sinkStream.stream)
        } catch {
            os_log(.error, log: log, "add streams: %{public}@", error.localizedDescription)
        }
    }

    var availableProperties: Set<CMIOExtensionProperty> {
        [.deviceTransportType, .deviceModel]
    }

    func deviceProperties(
        forProperties properties: Set<CMIOExtensionProperty>
    ) throws -> CMIOExtensionDeviceProperties {
        let deviceProperties = CMIOExtensionDeviceProperties(dictionary: [:])

        if properties.contains(.deviceTransportType) {
            deviceProperties.transportType = kIOAudioDeviceTransportTypeVirtual
        }

        if properties.contains(.deviceModel) {
            deviceProperties.model = cameraName
        }

        return deviceProperties
    }

    func setDeviceProperties(_ deviceProperties: CMIOExtensionDeviceProperties) throws {}

    // MARK: source side

    func startSourceStream() {
        let interval = 1.0 / Double(framesPerSecond)

        let timer = DispatchSource.makeTimerSource(flags: .strict, queue: timerQueue)
        timer.schedule(deadline: .now(), repeating: interval, leeway: .milliseconds(2))
        timer.setEventHandler { [weak self] in self?.emitFrame() }
        timer.resume()
        self.timer = timer
    }

    func stopSourceStream() {
        timer?.cancel()
        timer = nil
    }

    // MARK: sink side

    func sinkStopped() {
        frameLock.lock()
        latestFrame = nil
        frameLock.unlock()
    }

    func consume(_ sampleBuffer: CMSampleBuffer) {
        guard let pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }

        frameLock.lock()
        latestFrame = pixelBuffer
        frameLock.unlock()
    }

    // emitFrame serves the feeder's latest frame, or the splash.
    private func emitFrame() {
        frameLock.lock()
        let frame = latestFrame
        frameLock.unlock()

        let pixelBuffer = frame ?? splashFrame()
        guard let pixelBuffer else { return }

        var timing = CMSampleTimingInfo(
            duration: CMTime(value: 1, timescale: Int32(framesPerSecond)),
            presentationTimeStamp: CMClockGetTime(CMClockGetHostTimeClock()),
            decodeTimeStamp: .invalid)

        var sampleBuffer: CMSampleBuffer?
        var description: CMFormatDescription?
        CMVideoFormatDescriptionCreateForImageBuffer(
            allocator: kCFAllocatorDefault, imageBuffer: pixelBuffer,
            formatDescriptionOut: &description)

        guard let description else { return }

        CMSampleBufferCreateReadyWithImageBuffer(
            allocator: kCFAllocatorDefault, imageBuffer: pixelBuffer,
            formatDescription: description, sampleTiming: &timing,
            sampleBufferOut: &sampleBuffer)

        if let sampleBuffer {
            sourceStream.stream.send(
                sampleBuffer,
                discontinuity: [],
                hostTimeInNanoseconds: UInt64(
                    timing.presentationTimeStamp.seconds * Double(NSEC_PER_SEC)))
        }
    }

    // splashFrame renders the idle pattern: the Prukka helmet mark on a dark field.
    private func splashFrame() -> CVPixelBuffer? {
        var pixelBuffer: CVPixelBuffer?
        CVPixelBufferPoolCreatePixelBuffer(kCFAllocatorDefault, bufferPool, &pixelBuffer)

        guard let pixelBuffer else { return nil }

        CVPixelBufferLockBaseAddress(pixelBuffer, [])
        defer { CVPixelBufferUnlockBaseAddress(pixelBuffer, []) }

        guard let base = CVPixelBufferGetBaseAddress(pixelBuffer) else { return nil }

        let bytesPerRow = CVPixelBufferGetBytesPerRow(pixelBuffer)
        let buffer = base.assumingMemoryBound(to: UInt32.self)

        // ARGB literals land in BGRA memory on little-endian hosts.
        let dark: UInt32 = 0xFF16_1B23
        let teal: UInt32 = 0xFF0F_766E
        let white: UInt32 = 0xFFFF_FFFF

        for y in 0..<frameHeight {
            let row = buffer.advanced(by: y * bytesPerRow / 4)

            for x in 0..<frameWidth {
                row[x] = dark
            }
        }

        func fillPolygon(_ points: [(x: Int, y: Int)], color: UInt32) {
            guard points.count > 2 else { return }

            let minY = max(0, points.map { $0.y }.min() ?? 0)
            let maxY = min(frameHeight - 1, points.map { $0.y }.max() ?? 0)
            guard minY <= maxY else { return }

            for y in minY...maxY {
                var nodes: [Int] = []
                var previous = points.count - 1

                for current in 0..<points.count {
                    let a = points[current]
                    let b = points[previous]

                    if (a.y < y && b.y >= y) || (b.y < y && a.y >= y) {
                        let x = a.x + (y - a.y) * (b.x - a.x) / (b.y - a.y)
                        nodes.append(x)
                    }

                    previous = current
                }

                nodes.sort()

                var index = 0
                while index + 1 < nodes.count {
                    let minX = max(0, nodes[index])
                    let maxX = min(frameWidth - 1, nodes[index + 1])

                    if minX <= maxX {
                        let row = buffer.advanced(by: y * bytesPerRow / 4)

                        for x in minX...maxX {
                            row[x] = color
                        }
                    }

                    index += 2
                }
            }
        }

        func fillEllipse(centerX: Int, centerY: Int, radiusX: Int, radiusY: Int, color: UInt32) {
            guard radiusX > 0, radiusY > 0 else { return }

            let minX = max(0, centerX - radiusX)
            let maxX = min(frameWidth - 1, centerX + radiusX)
            let minY = max(0, centerY - radiusY)
            let maxY = min(frameHeight - 1, centerY + radiusY)
            let radiusX2 = radiusX * radiusX
            let radiusY2 = radiusY * radiusY
            let edge = radiusX2 * radiusY2

            for y in minY...maxY {
                let row = buffer.advanced(by: y * bytesPerRow / 4)
                let dy = y - centerY

                for x in minX...maxX {
                    let dx = x - centerX

                    if dx * dx * radiusY2 + dy * dy * radiusX2 <= edge {
                        row[x] = color
                    }
                }
            }
        }

        let scale = max(1, min(frameWidth, frameHeight) / 256)
        let centerX = frameWidth / 2
        let centerY = frameHeight / 2

        func point(_ x: Int, _ y: Int) -> (x: Int, y: Int) {
            (x: centerX + (x - 128) * scale, y: centerY + (y - 128) * scale)
        }

        func polygon(_ points: [(Int, Int)]) -> [(x: Int, y: Int)] {
            points.map { point($0.0, $0.1) }
        }

        fillPolygon(polygon([
            (140, 108), (112, 72), (130, 50), (150, 68), (178, 89),
            (227, 98), (253, 142), (237, 168), (228, 207), (197, 209),
            (182, 158), (141, 140), (130, 117),
        ]), color: teal)
        fillPolygon(polygon([
            (143, 114), (122, 73), (130, 59), (146, 76), (176, 95),
            (223, 106), (244, 126), (244, 145), (229, 167), (235, 184),
            (225, 198), (205, 199), (190, 153), (145, 133), (137, 119),
        ]), color: white)
        fillPolygon(polygon([
            (107, 151), (133, 132), (178, 139), (204, 170), (215, 190),
            (194, 210), (150, 216), (94, 203), (82, 190), (92, 165),
        ]), color: teal)
        fillPolygon(polygon([
            (120, 178), (100, 163), (74, 159), (62, 149), (53, 129),
            (31, 109), (11, 72), (40, 66), (70, 97), (108, 107),
            (149, 111), (188, 141), (164, 183),
        ]), color: teal)
        fillPolygon(polygon([
            (118, 169), (100, 155), (77, 151), (67, 143), (62, 124),
            (40, 104), (21, 75), (35, 72), (65, 101), (107, 114),
            (148, 118), (179, 141), (159, 174), (136, 181),
        ]), color: white)
        fillEllipse(
            centerX: point(116, 174).x,
            centerY: point(116, 174).y,
            radiusX: 28 * scale,
            radiusY: 28 * scale,
            color: teal)
        fillEllipse(
            centerX: point(116, 174).x,
            centerY: point(116, 174).y,
            radiusX: 22 * scale,
            radiusY: 22 * scale,
            color: white)
        fillEllipse(
            centerX: point(116, 174).x,
            centerY: point(116, 174).y,
            radiusX: 12 * scale,
            radiusY: 12 * scale,
            color: teal)

        return pixelBuffer
    }
}

// MARK: - Source stream

class SourceStreamSource: NSObject, CMIOExtensionStreamSource {
    private(set) var stream: CMIOExtensionStream!
    private let streamFormat: CMIOExtensionStreamFormat
    private weak var deviceSource: CameraDeviceSource?

    init(
        localizedName: String, streamID: UUID,
        streamFormat: CMIOExtensionStreamFormat, device: CameraDeviceSource
    ) {
        self.streamFormat = streamFormat
        self.deviceSource = device
        super.init()

        stream = CMIOExtensionStream(
            localizedName: localizedName, streamID: streamID,
            direction: .source, clockType: .hostTime, source: self)
    }

    var formats: [CMIOExtensionStreamFormat] { [streamFormat] }

    var availableProperties: Set<CMIOExtensionProperty> {
        [.streamActiveFormatIndex, .streamFrameDuration]
    }

    func streamProperties(
        forProperties properties: Set<CMIOExtensionProperty>
    ) throws -> CMIOExtensionStreamProperties {
        let streamProperties = CMIOExtensionStreamProperties(dictionary: [:])

        if properties.contains(.streamActiveFormatIndex) {
            streamProperties.activeFormatIndex = 0
        }

        if properties.contains(.streamFrameDuration) {
            streamProperties.frameDuration = CMTime(
                value: 1, timescale: Int32(framesPerSecond))
        }

        return streamProperties
    }

    func setStreamProperties(_ streamProperties: CMIOExtensionStreamProperties) throws {}

    func authorizedToStartStream(for client: CMIOExtensionClient) -> Bool { true }

    func startStream() throws {
        deviceSource?.startSourceStream()
    }

    func stopStream() throws {
        deviceSource?.stopSourceStream()
    }
}

// MARK: - Sink stream

class SinkStreamSource: NSObject, CMIOExtensionStreamSource {
    private(set) var stream: CMIOExtensionStream!
    private let streamFormat: CMIOExtensionStreamFormat
    private weak var deviceSource: CameraDeviceSource?
    private var client: CMIOExtensionClient?

    init(
        localizedName: String, streamID: UUID,
        streamFormat: CMIOExtensionStreamFormat, device: CameraDeviceSource
    ) {
        self.streamFormat = streamFormat
        self.deviceSource = device
        super.init()

        stream = CMIOExtensionStream(
            localizedName: localizedName, streamID: streamID,
            direction: .sink, clockType: .hostTime, source: self)
    }

    var formats: [CMIOExtensionStreamFormat] { [streamFormat] }

    var availableProperties: Set<CMIOExtensionProperty> {
        [.streamActiveFormatIndex, .streamFrameDuration, .streamSinkBufferQueueSize]
    }

    func streamProperties(
        forProperties properties: Set<CMIOExtensionProperty>
    ) throws -> CMIOExtensionStreamProperties {
        let streamProperties = CMIOExtensionStreamProperties(dictionary: [:])

        if properties.contains(.streamActiveFormatIndex) {
            streamProperties.activeFormatIndex = 0
        }

        if properties.contains(.streamFrameDuration) {
            streamProperties.frameDuration = CMTime(
                value: 1, timescale: Int32(framesPerSecond))
        }

        if properties.contains(.streamSinkBufferQueueSize) {
            streamProperties.sinkBufferQueueSize = 4
        }

        return streamProperties
    }

    func setStreamProperties(_ streamProperties: CMIOExtensionStreamProperties) throws {}

    func authorizedToStartStream(for client: CMIOExtensionClient) -> Bool {
        self.client = client

        return true
    }

    func startStream() throws {
        pump()
    }

    func stopStream() throws {
        client = nil
        deviceSource?.sinkStopped()
    }

    // pump pulls sample buffers the feeder enqueues, one at a time.
    private func pump() {
        guard let client else { return }

        stream.consumeSampleBuffer(from: client) { [weak self] buffer, _, _, _, _ in
            guard let self else { return }

            if let buffer {
                self.deviceSource?.consume(buffer)
                self.stream.notifyScheduledOutputChanged(
                    CMIOExtensionScheduledOutput(
                        sequenceNumber: 0,
                        hostTimeInNanoseconds: UInt64(
                            CMClockGetTime(CMClockGetHostTimeClock()).seconds
                                * Double(NSEC_PER_SEC))))
            }

            self.pump()
        }
    }
}
