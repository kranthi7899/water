import AppKit
import UserNotifications
import WaterClientCore

/// Native notifications (Slice V-notify, docs/slices/V.md §7.4). Every 30s
/// it polls the daemon's native-only `GET /v1/notifications?undelivered=1`
/// on its own socket client, lets `NotificationPlanner` pick what to show (at
/// most 3 banners plus one "and N more"), posts them through
/// `UNUserNotificationCenter`, and marks each delivered only after the OS
/// accepted it. A tap anchors a thread to the record
/// (`POST /v1/threads/anchor`, get-or-create) and opens it in the workspace;
/// when the record is gone (a 404: the card was dismissed, the envelope
/// expired) it opens the Decisions or Approvals view at that record instead.
///
/// Permission is asked on the first notification there is to show, never at
/// launch. A banner's `userInfo` holds only the record type and id; its text
/// is plain (UNNotificationContent renders no markup).
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let pollInterval: TimeInterval = 30

    /// Where a tap lands: `open(view:id:)` on the workspace window.
    var onOpen: ((_ view: String, _ id: String?) -> Void)?

    private let client: UnixSocketClient
    private let tokens: TokenProvider
    private let center: UNUserNotificationCenter
    /// Serial: the planner, the auth state and every socket call live here.
    private let work = DispatchQueue(label: "water.notifier", qos: .utility)
    private var planner = NotificationPlanner()
    private var timer: Timer?

    private enum Auth { case unknown, asking, granted, refused }
    private var auth = Auth.unknown
    /// requestAuthorization is called at most once per run; after that a
    /// refusal is re-checked from settings only (the owner may turn
    /// notifications on in System Settings).
    private var askedThisRun = false
    private var lastLogged: String?

    /// Nil when the process isn't a bundled app (e.g. `swift run`), where
    /// UNUserNotificationCenter.current() would trap.
    static func makeIfBundled(client: UnixSocketClient, tokens: TokenProvider) -> Notifier? {
        guard Bundle.main.bundleIdentifier != nil, Bundle.main.bundleURL.pathExtension == "app" else {
            NSLog("water notifier: not running as a bundled app; notifications are off")
            return nil
        }
        return Notifier(client: client, tokens: tokens)
    }

    private init(client: UnixSocketClient, tokens: TokenProvider) {
        self.client = client
        self.tokens = tokens
        center = UNUserNotificationCenter.current()
        super.init()
        center.delegate = self
    }

    func start() {
        guard timer == nil else { return }
        let t = Timer(timeInterval: Self.pollInterval, repeats: true) { [weak self] _ in self?.pollSoon() }
        t.tolerance = 5
        RunLoop.main.add(t, forMode: .common)
        timer = t
        // A first look shortly after launch, not a full interval later.
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) { [weak self] in self?.pollSoon() }
    }

    private func pollSoon() { work.async { [weak self] in self?.poll() } }

    // MARK: poll (on `work`)

    private func poll() {
        guard let token = try? tokens.token() else { return log("no client token yet") }
        let list: [PendingNotification]
        do {
            list = try client.fetchUndeliveredNotifications(token: token)
        } catch {
            return log("poll failed: \(error.localizedDescription)")
        }
        let plan = planner.plan(list)
        if !plan.rejected.isEmpty { log("ignoring \(plan.rejected.count) notification(s) with a malformed id") }
        for id in plan.remark { mark(id, token: token) }
        guard !plan.banners.isEmpty else { return }
        withAuthorization { [weak self] ok in
            guard let self else { return }
            for b in plan.banners {
                if ok { self.post(b, token: token) } else { self.planner.postFailed(b) }
            }
        }
    }

    /// Calls `then(granted)` on `work`. Asks for permission only when the
    /// status is still undetermined, i.e. at the first notification to show.
    private func withAuthorization(_ then: @escaping (Bool) -> Void) {
        if auth == .granted { return then(true) }
        if auth == .asking { return then(false) } // the prompt is still up; retry next poll
        center.getNotificationSettings { [weak self] s in
            self?.work.async {
                guard let self else { return }
                switch s.authorizationStatus {
                case .authorized, .provisional:
                    self.auth = .granted
                    then(true)
                case .notDetermined where !self.askedThisRun:
                    self.askedThisRun = true
                    self.auth = .asking
                    then(false) // this poll's banners retry once the answer is in
                    self.center.requestAuthorization(options: [.alert, .sound]) { granted, error in
                        self.work.async {
                            self.auth = granted ? .granted : .refused
                            if let error { self.log("notification permission refused: \(error.localizedDescription)") }
                            else if !granted { self.log("notifications turned off for Water") }
                            if granted { self.poll() }
                        }
                    }
                default:
                    self.auth = .refused
                    self.log("notifications are off for Water (System Settings > Notifications)")
                    then(false)
                }
            }
        }
    }

    private func post(_ b: NotificationBanner, token: String) {
        let content = UNMutableNotificationContent()
        content.title = b.title
        content.body = b.body
        content.userInfo = b.userInfo
        content.sound = .default
        content.threadIdentifier = "water.needs-you"
        let req = UNNotificationRequest(identifier: b.identifier, content: content, trigger: nil)
        center.add(req) { [weak self] error in
            self?.work.async {
                guard let self else { return }
                if let error {
                    self.planner.postFailed(b)
                    self.log("banner not shown: \(error.localizedDescription)")
                    return
                }
                self.planner.posted(b)
                for id in b.notificationIDs { self.mark(id, token: token) }
            }
        }
    }

    private func mark(_ id: String, token: String) {
        do {
            try client.markNotificationDelivered(id: id, token: token)
            planner.marked(id)
        } catch WaterClientError.http(status: 404, body: _) {
            planner.marked(id) // gone on the daemon: nothing left to mark
        } catch {
            log("mark delivered failed (retried next poll): \(error.localizedDescription)")
        }
    }

    /// One line per distinct message, so a stopped daemon doesn't log every
    /// 30 seconds.
    private func log(_ s: String) {
        guard s != lastLogged else { return }
        lastLogged = s
        NSLog("water notifier: %@", s)
    }

    // MARK: UNUserNotificationCenterDelegate

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        // Show it even while Water's own window is in front.
        completionHandler([.banner, .list, .sound])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        guard response.actionIdentifier == UNNotificationDefaultActionIdentifier else { return completionHandler() }
        let anchor = NotificationAnchor(userInfo: response.notification.request.content.userInfo)
        work.async { [weak self] in
            guard let self else { return DispatchQueue.main.async(execute: completionHandler) }
            let target = self.client.notificationTapTarget(anchor, token: try? self.tokens.token())
            DispatchQueue.main.async {
                switch target {
                case .thread(let id): self.onOpen?("threads", id)
                case .view(let view, let id): self.onOpen?(view, id)
                }
                completionHandler()
            }
        }
    }
}
