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
// Exit codes: 0 clean end of stream, 1 startup/config error, 2 device change
// needing a respawn, 3 permanent authorization denial (respawning cannot fix
// it; only the user granting microphone access can).
//
// Args: [--play] --device <localized name or substring> --rate <hz>.

signal(SIGPIPE, SIG_IGN)

var deviceName = ""
var rate: Double = 16000
var playback = false

// tag is computed so die() is usable while arguments are still being parsed.
var tag: String { playback ? "micplay" : "miccapture" }

func die(_ m: String) -> Never {
    FileHandle.standardError.write(Data((tag + ": " + m + "\n").utf8))
    exit(1)
}

func note(_ m: String) {
    FileHandle.standardError.write(Data((tag + ": " + m + "\n").utf8))
}

// Strict parsing: a typoed flag, a missing value or a malformed rate must
// fail fast, not silently run on the default device at the default rate.
var a = 1
let argv = CommandLine.arguments
while a < argv.count {
    switch argv[a] {
    case "--device":
        guard a + 1 < argv.count else { die("--device needs a value") }
        deviceName = argv[a + 1]
        a += 1
    case "--rate":
        guard a + 1 < argv.count, let parsed = Double(argv[a + 1]), parsed > 0 else {
            die("--rate needs a positive Hz value")
        }
        rate = parsed
        a += 1
    case "--play":
        playback = true
    default:
        die("unknown argument: \(argv[a])")
    }
    a += 1
}

// MARK: - Playback (--play)

// outputDeviceID resolves an output device by localized name, exact match
// first, then substring. Only devices with output channels qualify, so an
// input/output pair sharing a name (the virtual devices do not, but
// Continuity pairs may) cannot misbind the render side.
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
    // The call rewrites size with the bytes actually delivered; a device
    // vanishing between the two calls would otherwise leave stale zero IDs
    // probed as if they were devices.
    devices.removeLast(devices.count - Int(size) / MemoryLayout<AudioDeviceID>.size)

    // The explicit pointer scope keeps the generic sound: an implicit
    // &value-to-raw-pointer conversion is flagged for T that may hold
    // object references (CFString here).
    func property<T>(_ device: AudioDeviceID, _ selector: AudioObjectPropertySelector,
                     _ scope: AudioObjectPropertyScope, _ value: inout T) -> Bool {
        var addr = AudioObjectPropertyAddress(
            mSelector: selector, mScope: scope, mElement: kAudioObjectPropertyElementMain)
        var sz = UInt32(MemoryLayout<T>.size)
        return withUnsafeMutablePointer(to: &value) { ptr in
            AudioObjectGetPropertyData(device, &addr, 0, nil, &sz, UnsafeMutableRawPointer(ptr)) == noErr
        }
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
//
// The render callback runs on the realtime audio thread, so the ring is
// guarded by os_unfair_lock — Apple's primitive with priority donation for
// exactly this shape: a tiny critical section shared with a realtime thread.
// An ordinary NSLock/pthread mutex there risks priority inversion (the audio
// thread spinning behind a preempted feeder) and audible glitches. The lock
// lives on the heap: locking through &property would trip Swift's exclusivity
// checking. Capacity is a power of two so the cursors wrap with a mask
// instead of a division per sample.
final class PlayRing {
    private var buf: [Int16]
    private let mask: Int
    private var head = 0 // consumer cursor
    private var tail = 0 // producer cursor
    private let lock: UnsafeMutablePointer<os_unfair_lock_s>
    // Full-scale symmetric conversion: 1/32768 maps Int16.min to exactly -1.
    private let scale = Float(1.0 / 32768.0)

    // init rounds the requested capacity up to the next power of two.
    init(capacity: Int) {
        var size = 1
        while size < capacity { size <<= 1 }
        buf = [Int16](repeating: 0, count: size)
        mask = size - 1
        lock = .allocate(capacity: 1)
        lock.initialize(to: os_unfair_lock_s())
    }

    deinit {
        lock.deinitialize(count: 1)
        lock.deallocate()
    }

    var isEmpty: Bool {
        os_unfair_lock_lock(lock)
        defer { os_unfair_lock_unlock(lock) }
        return head == tail
    }

    func push(_ samples: [Int16]) {
        os_unfair_lock_lock(lock)
        defer { os_unfair_lock_unlock(lock) }
        for s in samples {
            buf[tail & mask] = s
            tail += 1
            if tail - head > buf.count { head = tail - buf.count } // overwrite stalest
        }
    }

    // pop fills out with up to count samples and returns how many carried
    // audio; the remainder is zeroed.
    func pop(into out: UnsafeMutablePointer<Float>, count: Int) -> Int {
        os_unfair_lock_lock(lock)
        defer { os_unfair_lock_unlock(lock) }
        let filled = min(count, tail - head)
        for i in 0..<filled {
            out[i] = Float(buf[head & mask]) * scale
            head += 1
        }
        for i in filled..<count {
            out[i] = 0
        }
        return filled
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

    let ring = PlayRing(capacity: Int(rate) * 2) // ~2 s of headroom
    // The isSilence hint is deliberately NEVER set: zeros are written as
    // real frames every cycle. Letting the engine skip device writes
    // during silence detaches the writer from the device timeline — the
    // exact push-model drift the pull design exists to prevent — and a
    // capture client joining mid-silence then reads stale ring
    // generations as permanent zero.
    let source = AVAudioSourceNode(format: format) { _, _, frameCount, audioBufferList -> OSStatus in
        let buffers = UnsafeMutableAudioBufferListPointer(audioBufferList)
        guard let data = buffers[0].mData else { return noErr }
        _ = ring.pop(into: data.assumingMemoryBound(to: Float.self), count: Int(frameCount))
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

    // A configuration change (device died, sample rate switched by another
    // app, ownership moved) stops the engine. The canonical recovery is to
    // re-resolve the NAME — wherever the device lives in the table now — and
    // restart; only an unresolvable name gives up, letting the daemon respawn
    // a fresh helper. The observer is registered BEFORE the first start and
    // the start itself runs on the same serial queue: a change landing in a
    // register-after-start window would leave the engine silently stopped
    // forever, and all engine mutation on one queue cannot race itself. The
    // setup's own spurious notification just runs one idempotent rebind.
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
    rebindQueue.sync {
        guard startBound(deviceID) else { die("cannot bind the output unit to \(deviceName)") }
    }
    note("rendering to \(deviceName) (id \(deviceID)) at \(Int(rate)) Hz mono s16le")

    // The stdin thread only feeds the ring; the render callback does the
    // rest. EOF drains whatever the ring still holds — up to its full two
    // seconds — before exiting, so the tail of the dub is never cut.
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
        let deadline = Date().addingTimeInterval(2.5)
        while !ring.isEmpty && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.05)
        }
        exit(0)
    }
    dispatchMain()
}

if playback { runPlayback() }

// MARK: - Capture (default)

// requestAccess returns immediately with the current status when a grant (or
// denial) already exists — the launchd case — and prompts only in an
// interactive session. A denial is PERMANENT from the helper's point of view:
// exit 3 tells the supervisor that respawning cannot fix it.
let sem = DispatchSemaphore(value: 0)
AVCaptureDevice.requestAccess(for: .audio) { _ in sem.signal() }
sem.wait()
guard AVCaptureDevice.authorizationStatus(for: .audio) == .authorized else {
    note("microphone access not authorized; grant it in System Settings > Privacy")
    exit(3)
}

// Exact name first, then substring: a single-pass mixed predicate would let
// an earlier substring hit ("MacBook Pro Microphone (2)") beat a later exact
// match ("Microphone"). An empty or unmatched name falls back to the default
// input device.
let discovery = AVCaptureDevice.DiscoverySession(
    deviceTypes: [.builtInMicrophone, .externalUnknown], mediaType: .audio, position: .unspecified)
let device = discovery.devices.first(where: { $0.localizedName == deviceName })
    ?? discovery.devices.first(where: { deviceName.isEmpty || $0.localizedName.contains(deviceName) })
    ?? AVCaptureDevice.default(for: .audio)
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
    func captureOutput(_ o: AVCaptureOutput, didOutput sb: CMSampleBuffer, from c: AVCaptureConnection) {
        guard let bb = CMSampleBufferGetDataBuffer(sb) else { return }
        var length = 0
        var ptr: UnsafeMutablePointer<Int8>?
        guard CMBlockBufferGetDataPointer(
            bb, atOffset: 0, lengthAtOffsetOut: nil, totalLengthOut: &length, dataPointerOut: &ptr) == noErr,
            let p = ptr, length > 0 else { return }
        // Raw POSIX writes: the legacy FileHandle.write raises an ObjC
        // exception on EPIPE (SIGPIPE is ignored above) and aborts the whole
        // helper with SIGABRT; write(2) lets a closed pipe end the stream
        // cleanly, and copies nothing on the per-callback path.
        var off = 0
        while off < length {
            let n = write(STDOUT_FILENO, p + off, length - off)
            if n <= 0 {
                exit(errno == EPIPE ? 0 : 1)
            }
            off += n
        }
    }
}

let writer = Writer()
output.setSampleBufferDelegate(writer, queue: DispatchQueue(label: "io.prukka.miccapture"))
guard session.canAddOutput(output) else { die("cannot add output") }
session.addOutput(output)

// A dead device must not leave the helper alive streaming nothing: the
// supervisor only reacts to process exit, so exit for a clean respawn (the
// name re-resolves against wherever the device table stands then).
NotificationCenter.default.addObserver(
    forName: .AVCaptureSessionRuntimeError, object: session, queue: nil) { notification in
    note("capture runtime error: \(String(describing: notification.userInfo?[AVCaptureSessionErrorKey]))")
    exit(2)
}
NotificationCenter.default.addObserver(
    forName: .AVCaptureDeviceWasDisconnected, object: dev, queue: nil) { _ in
    note("\(dev.localizedName) disconnected; exiting for a clean respawn")
    exit(2)
}

session.startRunning()
note("streaming \(dev.localizedName) at \(Int(rate)) Hz mono s16le")
dispatchMain()
