import AVFoundation
import CoreAudio
import Foundation

// prukka-miccapture: the daemon's native audio-device bridge, two modes.
//
// Capture (default): request microphone authorization, capture one input
// device through AVFoundation and stream reference-format PCM (s16le, mono)
// to stdout. It replaces ffmpeg's raw AVFoundation input: macOS delivers
// silent buffers to a process that opens a capture device without calling
// AVCaptureDevice.requestAccess, which is exactly what happens to ffmpeg when
// launchd starts the daemon.
//
// Playback (--play): read s16le mono PCM from stdin and render it to one
// OUTPUT device resolved by its CoreAudio name. It replaces ffmpeg's
// audiotoolbox muxer, which can address a device only by its position in the
// system array — a position Continuity devices reshuffle at will. Binding by
// name is stable; when the device dies or the engine's configuration changes,
// the helper EXITS nonzero so the daemon's reopen path respawns it and the
// name resolves to wherever the device lives now.
//
// Args: [--play] --device <localized name or substring> --rate <hz>.

signal(SIGPIPE, SIG_IGN)

var deviceName = ""
var rate: Double = 16000
var playback = false
var a = 1
let argv = CommandLine.arguments
while a < argv.count {
    switch argv[a] {
    case "--device": if a + 1 < argv.count { deviceName = argv[a + 1]; a += 1 }
    case "--rate": if a + 1 < argv.count { rate = Double(argv[a + 1]) ?? 16000; a += 1 }
    case "--play": playback = true
    default: break
    }
    a += 1
}

let tag = playback ? "micplay" : "miccapture"

func die(_ m: String) -> Never {
    FileHandle.standardError.write((tag + ": " + m + "\n").data(using: .utf8)!)
    exit(1)
}

func note(_ m: String) {
    FileHandle.standardError.write((tag + ": " + m + "\n").data(using: .utf8)!)
}

// MARK: - Playback (--play)

// outputDeviceID resolves an output device by localized name, exact match
// first, then substring — the same policy the capture path applies. Only
// devices with output channels qualify, so an input/output pair sharing a
// name (the virtual devices do not, but Continuity pairs may) cannot
// misbind the render side.
func outputDeviceID(named name: String) -> AudioDeviceID? {
    var address = AudioObjectPropertyAddress(
        mSelector: kAudioHardwarePropertyDevices,
        mScope: kAudioObjectPropertyScopeGlobal,
        mElement: kAudioObjectPropertyElementMain)
    var size: UInt32 = 0
    guard AudioObjectGetPropertyDataSize(
        AudioObjectID(kAudioObjectSystemObject), &address, 0, nil, &size) == noErr else { return nil }
    var devices = [AudioDeviceID](repeating: 0, count: Int(size) / MemoryLayout<AudioDeviceID>.size)
    guard AudioObjectGetPropertyData(
        AudioObjectID(kAudioObjectSystemObject), &address, 0, nil, &size, &devices) == noErr else { return nil }

    func property<T>(_ device: AudioDeviceID, _ selector: AudioObjectPropertySelector,
                     _ scope: AudioObjectPropertyScope, _ value: inout T) -> Bool {
        var addr = AudioObjectPropertyAddress(
            mSelector: selector, mScope: scope, mElement: kAudioObjectPropertyElementMain)
        var sz = UInt32(MemoryLayout<T>.size)
        return AudioObjectGetPropertyData(device, &addr, 0, nil, &sz, &value) == noErr
    }

    func outputChannels(_ device: AudioDeviceID) -> Int {
        var addr = AudioObjectPropertyAddress(
            mSelector: kAudioDevicePropertyStreamConfiguration,
            mScope: kAudioDevicePropertyScopeOutput,
            mElement: kAudioObjectPropertyElementMain)
        var sz: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(device, &addr, 0, nil, &sz) == noErr, sz > 0 else { return 0 }
        let list = UnsafeMutableRawPointer.allocate(
            byteCount: Int(sz), alignment: MemoryLayout<AudioBufferList>.alignment)
        defer { list.deallocate() }
        guard AudioObjectGetPropertyData(
            device, &addr, 0, nil, &sz, list) == noErr else { return 0 }
        let buffers = UnsafeMutableAudioBufferListPointer(list.assumingMemoryBound(to: AudioBufferList.self))
        return buffers.reduce(0) { $0 + Int($1.mNumberChannels) }
    }

    func deviceName(_ device: AudioDeviceID) -> String {
        var cfName: CFString = "" as CFString
        return property(device, kAudioObjectPropertyName, kAudioObjectPropertyScopeGlobal, &cfName)
            ? cfName as String : ""
    }

    let outputs = devices.filter { outputChannels($0) > 0 }
    if let exact = outputs.first(where: { deviceName($0) == name }) { return exact }
    return outputs.first(where: { deviceName($0).contains(name) })
}

// PlayRing is a single-producer single-consumer ring: the stdin thread fills
// it, the render callback drains it, and an empty ring yields silence. Pull
// rendering never underruns at the device: the engine's HAL client asks every
// cycle and always gets an answer, so its device timeline never drifts — the
// failure mode that silently killed push-model (scheduled-buffer) playback.
final class PlayRing {
    private var buf: [Int16]
    private var head = 0 // consumer cursor
    private var tail = 0 // producer cursor
    private let lock = NSLock()

    init(capacity: Int) { buf = [Int16](repeating: 0, count: capacity) }

    func push(_ samples: [Int16]) {
        lock.lock()
        defer { lock.unlock() }
        for s in samples {
            buf[tail % buf.count] = s
            tail += 1
            if tail - head > buf.count { head = tail - buf.count } // overwrite stalest
        }
    }

    func pop(into out: UnsafeMutablePointer<Float>, count: Int) {
        lock.lock()
        defer { lock.unlock() }
        for i in 0..<count {
            if head < tail {
                out[i] = Float(buf[head % buf.count]) / Float(Int16.max)
                head += 1
            } else {
                out[i] = 0
            }
        }
    }
}

func runPlayback() -> Never {
    guard !deviceName.isEmpty else { die("--device is required") }
    guard let deviceID = outputDeviceID(named: deviceName) else {
        die("no output device matching \(deviceName)")
    }

    let engine = AVAudioEngine()
    guard let format = AVAudioFormat(
        commonFormat: .pcmFormatFloat32, sampleRate: rate, channels: 1, interleaved: false) else {
        die("cannot build the render format")
    }

    let ring = PlayRing(capacity: Int(rate) * 2) // 2 s of headroom
    let source = AVAudioSourceNode(format: format) { _, _, frameCount, audioBufferList -> OSStatus in
        let buffers = UnsafeMutableAudioBufferListPointer(audioBufferList)
        guard let data = buffers[0].mData else { return noErr }
        ring.pop(into: data.assumingMemoryBound(to: Float.self), count: Int(frameCount))
        return noErr
    }
    engine.attach(source)
    engine.connect(source, to: engine.mainMixerNode, format: format)

    func bind(_ id: AudioDeviceID) -> Bool {
        var device = id
        guard let unit = engine.outputNode.audioUnit else { return false }
        return AudioUnitSetProperty(
            unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0,
            &device, UInt32(MemoryLayout<AudioDeviceID>.size)) == noErr
    }

    // boundDevice reads back which device the output unit is ACTUALLY on:
    // AVAudioEngine can rebuild its graph across stop/start and silently fall
    // back to the default output, which would leak the dub to the speakers.
    func boundDevice() -> AudioDeviceID {
        guard let unit = engine.outputNode.audioUnit else { return 0 }
        var device: AudioDeviceID = 0
        var size = UInt32(MemoryLayout<AudioDeviceID>.size)
        guard AudioUnitGetProperty(
            unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0,
            &device, &size) == noErr else { return 0 }
        return device
    }

    func startBound(_ id: AudioDeviceID) -> Bool {
        guard bind(id), (try? engine.start()) != nil else { return false }
        guard boundDevice() == id else {
            note("output unit drifted off \(deviceName) after start; rebinding")
            engine.stop()
            return bind(id) && (try? engine.start()) != nil && boundDevice() == id
        }
        return true
    }

    guard startBound(deviceID) else { die("cannot bind the output unit to \(deviceName)") }
    note("rendering to \(deviceName) (id \(deviceID)) at \(Int(rate)) Hz mono s16le")

    // A configuration change (device died, sample rate switched by another
    // app, ownership moved) stops the engine. The canonical recovery is to
    // re-resolve the NAME — wherever the device lives in the table now — and
    // restart; only an unresolvable name gives up, letting the daemon respawn
    // a fresh helper. Registered after the first start: binding a non-default
    // device posts one spurious change during setup. The source node needs no
    // re-arming: pull rendering resumes with the engine.
    let rebindQueue = DispatchQueue(label: "io.prukka.micplay.rebind")
    NotificationCenter.default.addObserver(
        forName: .AVAudioEngineConfigurationChange, object: engine, queue: nil) { _ in
        rebindQueue.async {
            engine.stop()
            guard let fresh = outputDeviceID(named: deviceName), startBound(fresh) else {
                note("rebind after configuration change failed; exiting for a clean respawn")
                exit(2)
            }
            note("rebound to \(deviceName) (id \(fresh))")
        }
    }

    // The stdin thread only feeds the ring; the render callback does the rest.
    // EOF gives the ring one second to drain, then exits cleanly.
    let input = FileHandle.standardInput
    DispatchQueue.global(qos: .userInteractive).async {
        let chunkBytes = 2 * max(1, Int(rate) / 50) // 20 ms of s16 mono
        while true {
            let data = input.readData(ofLength: chunkBytes)
            if data.isEmpty { break }
            var samples = [Int16](repeating: 0, count: data.count / 2)
            _ = samples.withUnsafeMutableBytes { data.copyBytes(to: $0) }
            ring.push(samples)
        }
        Thread.sleep(forTimeInterval: 1)
        exit(0)
    }
    dispatchMain()
}

if playback { runPlayback() }

// MARK: - Capture (default)

// requestAccess returns immediately with the current status when a grant (or
// denial) already exists — the launchd case — and prompts only in an
// interactive session. Either way, capture proceeds solely when authorized.
let sem = DispatchSemaphore(value: 0)
AVCaptureDevice.requestAccess(for: .audio) { _ in sem.signal() }
sem.wait()
guard AVCaptureDevice.authorizationStatus(for: .audio) == .authorized else {
    die("microphone access not authorized")
}

let discovery = AVCaptureDevice.DiscoverySession(
    deviceTypes: [.builtInMicrophone, .externalUnknown], mediaType: .audio, position: .unspecified)
let device = discovery.devices.first(where: {
    deviceName.isEmpty || $0.localizedName == deviceName || $0.localizedName.contains(deviceName)
}) ?? AVCaptureDevice.default(for: .audio)
guard let dev = device else { die("no audio device matching \(deviceName)") }

let session = AVCaptureSession()
guard let input = try? AVCaptureDeviceInput(device: dev), session.canAddInput(input) else {
    die("cannot open \(dev.localizedName)")
}
session.addInput(input)

// AVFoundation resamples the capture to this format; the daemon's PCM contract
// is 16 kHz mono signed 16-bit little-endian.
let output = AVCaptureAudioDataOutput()
output.audioSettings = [
    AVFormatIDKey: kAudioFormatLinearPCM,
    AVSampleRateKey: rate,
    AVNumberOfChannelsKey: 1,
    AVLinearPCMBitDepthKey: 16,
    AVLinearPCMIsFloatKey: false,
    AVLinearPCMIsBigEndianKey: false,
    AVLinearPCMIsNonInterleaved: false,
]

final class Writer: NSObject, AVCaptureAudioDataOutputSampleBufferDelegate {
    let out = FileHandle.standardOutput
    func captureOutput(_ o: AVCaptureOutput, didOutput sb: CMSampleBuffer, from c: AVCaptureConnection) {
        guard let bb = CMSampleBufferGetDataBuffer(sb) else { return }
        var length = 0
        var ptr: UnsafeMutablePointer<Int8>?
        guard CMBlockBufferGetDataPointer(
            bb, atOffset: 0, lengthAtOffsetOut: nil, totalLengthOut: &length, dataPointerOut: &ptr) == noErr,
            let p = ptr, length > 0 else { return }
        out.write(Data(bytes: p, count: length))
    }
}

let writer = Writer()
output.setSampleBufferDelegate(writer, queue: DispatchQueue(label: "io.prukka.miccapture"))
guard session.canAddOutput(output) else { die("cannot add output") }
session.addOutput(output)

session.startRunning()
note("streaming \(dev.localizedName) at \(Int(rate)) Hz mono s16le")
dispatchMain()
