import AVFoundation
import CoreAudio
import Foundation

/// Everything the Mac is playing (the other side of a call), as a stream of
/// in-memory PCM buffers: a Core Audio process tap (macOS 14.2+) wrapped in a
/// private aggregate device, read with an IO proc. Buffers are copied only so
/// the recognizer can hold them; nothing is written to disk.
///
/// macOS asks for "System Audio Recording" permission the first time the tap
/// runs (NSAudioCaptureUsageDescription). There is no API to check that
/// grant up front: if it is denied, the tap delivers silence.
@available(macOS 14.2, *)
final class SystemAudioTap {
    private var tapID = AudioObjectID(kAudioObjectUnknown)
    private var aggregateID = AudioObjectID(kAudioObjectUnknown)
    private var procID: AudioDeviceIOProcID?
    private let queue = DispatchQueue(label: "water.system-audio-tap", qos: .userInitiated)

    var isRunning: Bool { procID != nil }

    /// `onBuffer` runs on a private queue.
    func start(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws {
        stop()
        do {
            try create(onBuffer)
        } catch {
            stop()
            throw error
        }
    }

    private func create(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws {
        // Leave Water's own output (spoken replies) out of the mix.
        let own = Self.processObject(pid: getpid()).map { [$0] } ?? []
        let desc = CATapDescription(monoGlobalTapButExcludeProcesses: own)
        desc.uuid = UUID()
        desc.name = "Water meeting capture"
        desc.isPrivate = true
        desc.muteBehavior = .unmuted
        try check(AudioHardwareCreateProcessTap(desc, &tapID), "create the system audio tap")

        var asbd = AudioStreamBasicDescription()
        try getProperty(tapID, kAudioTapPropertyFormat, &asbd, "read the tap format")
        guard let format = AVAudioFormat(streamDescription: &asbd), asbd.mBytesPerFrame > 0 else {
            throw CaptureError("System audio has an unsupported format.")
        }

        var outputID = AudioObjectID(kAudioObjectUnknown)
        try getProperty(AudioObjectID(kAudioObjectSystemObject), kAudioHardwarePropertyDefaultSystemOutputDevice, &outputID, "find the output device")
        var uid: Unmanaged<CFString>?
        try getProperty(outputID, kAudioDevicePropertyDeviceUID, &uid, "read the output device")
        guard let outputUID = uid?.takeRetainedValue() as String? else {
            throw CaptureError("Couldn't read the output device.")
        }

        let aggregate: [String: Any] = [
            kAudioAggregateDeviceNameKey: "Water meeting capture",
            kAudioAggregateDeviceUIDKey: UUID().uuidString,
            kAudioAggregateDeviceMainSubDeviceKey: outputUID,
            kAudioAggregateDeviceIsPrivateKey: true,
            kAudioAggregateDeviceIsStackedKey: false,
            kAudioAggregateDeviceTapAutoStartKey: true,
            kAudioAggregateDeviceSubDeviceListKey: [[kAudioSubDeviceUIDKey: outputUID]],
            kAudioAggregateDeviceTapListKey: [[kAudioSubTapDriftCompensationKey: true,
                                               kAudioSubTapUIDKey: desc.uuid.uuidString]],
        ]
        try check(AudioHardwareCreateAggregateDevice(aggregate as CFDictionary, &aggregateID), "create the capture device")

        let bytesPerFrame = asbd.mBytesPerFrame
        let channels = Int(format.isInterleaved ? 1 : format.channelCount)
        try check(AudioDeviceCreateIOProcIDWithBlock(&procID, aggregateID, queue) { _, input, _, _, _ in
            let src = UnsafeMutableAudioBufferListPointer(UnsafeMutablePointer(mutating: input))
            // The tap's buffers come last in the aggregate's input list.
            guard src.count >= channels else { return }
            let tapBuffers = Array(src.suffix(channels))
            let frames = tapBuffers[0].mDataByteSize / bytesPerFrame
            guard frames > 0, let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: frames) else { return }
            out.frameLength = frames
            let dst = UnsafeMutableAudioBufferListPointer(out.mutableAudioBufferList)
            for (i, b) in tapBuffers.enumerated() where i < dst.count {
                guard let s = b.mData, let d = dst[i].mData else { continue }
                memcpy(d, s, Int(min(b.mDataByteSize, dst[i].mDataByteSize)))
            }
            onBuffer(out)
        }, "attach to the capture device")
        try check(AudioDeviceStart(aggregateID, procID), "start system audio capture")
    }

    func stop() {
        if aggregateID != kAudioObjectUnknown, let procID {
            AudioDeviceStop(aggregateID, procID)
            AudioDeviceDestroyIOProcID(aggregateID, procID)
        }
        procID = nil
        if aggregateID != kAudioObjectUnknown {
            AudioHardwareDestroyAggregateDevice(aggregateID)
            aggregateID = AudioObjectID(kAudioObjectUnknown)
        }
        if tapID != kAudioObjectUnknown {
            AudioHardwareDestroyProcessTap(tapID)
            tapID = AudioObjectID(kAudioObjectUnknown)
        }
    }

    deinit { stop() }

    private static func processObject(pid: pid_t) -> AudioObjectID? {
        var pid = pid
        var obj = AudioObjectID(kAudioObjectUnknown)
        var size = UInt32(MemoryLayout<AudioObjectID>.size)
        var addr = AudioObjectPropertyAddress(mSelector: kAudioHardwarePropertyTranslatePIDToProcessObject,
                                              mScope: kAudioObjectPropertyScopeGlobal,
                                              mElement: kAudioObjectPropertyElementMain)
        let st = AudioObjectGetPropertyData(AudioObjectID(kAudioObjectSystemObject), &addr,
                                            UInt32(MemoryLayout<pid_t>.size), &pid, &size, &obj)
        return st == noErr && obj != kAudioObjectUnknown ? obj : nil
    }

    private func getProperty<T>(_ object: AudioObjectID, _ selector: AudioObjectPropertySelector, _ value: inout T, _ what: String) throws {
        var addr = AudioObjectPropertyAddress(mSelector: selector,
                                              mScope: kAudioObjectPropertyScopeGlobal,
                                              mElement: kAudioObjectPropertyElementMain)
        var size = UInt32(MemoryLayout<T>.size)
        let st = withUnsafeMutablePointer(to: &value) { AudioObjectGetPropertyData(object, &addr, 0, nil, &size, $0) }
        try check(st, what)
    }

    private func check(_ status: OSStatus, _ what: String) throws {
        guard status == noErr else { throw CaptureError("Couldn't \(what) (OSStatus \(status)).") }
    }
}
