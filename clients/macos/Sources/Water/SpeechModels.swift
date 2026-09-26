import FluidAudio
import Foundation

/// Where Water keeps FluidAudio's models, and whether they're all there
/// (V-hud's §1 leftovers). Both managers get a `directory:` under
/// `~/Library/Application Support/Water/Models/`, so nothing lands in
/// FluidAudio's own default folders any more — except Kokoro's shared G2P
/// assets, which FluidAudio 0.17.4 hard-codes to `~/.cache/fluidaudio/`
/// regardless of any directory (disclosed in docs/slices/V.md §1).
enum SpeechModels {
    /// `~/Library/Application Support/Water/Models/`.
    static let root: URL = {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("Library/Application Support")
        return base.appendingPathComponent("Water", isDirectory: true)
            .appendingPathComponent("Models", isDirectory: true)
    }()

    /// Kokoro's `directory:` is the models root; it adds its own
    /// `kokoro-82m-coreml/ANE` folder under it.
    static var kokoroDirectory: URL { root }

    /// Parakeet's `directory:` is its repo folder itself (`AsrModels`
    /// downloads into the parent and reads `<parent>/<repo folder>`).
    static var parakeetDirectory: URL {
        root.appendingPathComponent(Repo.parakeetV3.folderName, isDirectory: true)
    }

    /// The one Kokoro manager for this process, pointed at Water's folder.
    static func makeKokoroManager() -> KokoroAneManager {
        KokoroAneManager(directory: kokoroDirectory)
    }

    /// True when every Parakeet v3 file, every Kokoro ANE file and the
    /// shared G2P assets are on disk — checked, not remembered, so a
    /// deleted folder is noticed and asked about again rather than
    /// re-downloaded silently on first use.
    static func allOnDisk() -> Bool {
        let fm = FileManager.default
        guard AsrModels.modelsExist(at: parakeetDirectory, version: .v3) else { return false }
        let kokoroRepo = root.appendingPathComponent(Repo.kokoroAne.folderName, isDirectory: true)
        guard ModelNames.KokoroAne.requiredModels.allSatisfy({
            fm.fileExists(atPath: kokoroRepo.appendingPathComponent($0).path)
        }) else { return false }
        guard let cache = try? TtsCacheDirectory.ensure() else { return false }
        let g2p = cache.appendingPathComponent("Models", isDirectory: true)
            .appendingPathComponent(Repo.kokoro.folderName, isDirectory: true)
        return ModelNames.G2P.requiredModels.allSatisfy { fm.fileExists(atPath: g2p.appendingPathComponent($0).path) }
    }
}
