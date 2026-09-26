import AppKit
import WaterClientCore
import WebKit

// The workspace window (Slice V-ui, docs/slices/V.md §5): a WKWebView
// showing the daemon's embedded web UI (internal/webui) at water://app/ui/.
//
// Transport: every request the page makes goes to the custom `water://`
// scheme, which WorkspaceSchemeHandler proxies into the daemon's existing
// Unix socket. There is no TCP port. The bearer token is added here, in
// native code, and never reaches JavaScript or a response header. Only the
// routes WorkspaceAllowlist (WaterClientCore, unit-tested) names are ever
// forwarded; everything else gets a 403 without touching the socket.

/// Proxies `water://app/...` requests into the daemon's Unix socket.
///
/// All `WKURLSchemeTask` calls happen on the main thread, and only while the
/// task is still in `live`: WebKit raises if a task is used after
/// `webView(_:stop:)`, and `stop` also runs on the main thread, so checking
/// `live` there is race-free.
final class WorkspaceSchemeHandler: NSObject, WKURLSchemeHandler {
    private let client: UnixSocketClient
    private let tokens: TokenProvider
    private var live: [ObjectIdentifier: CancelToken] = [:]

    init(client: UnixSocketClient, tokens: TokenProvider) {
        self.client = client
        self.tokens = tokens
    }

    func webView(_ webView: WKWebView, start task: WKURLSchemeTask) {
        let request = task.request
        guard let url = request.url else {
            send(task, status: 400, "no URL")
            return
        }
        let method = (request.httpMethod ?? "GET")
        let path: String
        let forwardMethod: String
        switch WorkspaceAllowlist.decide(method: method, url: url) {
        case .deny:
            // Never touches the socket. The reason stays out of the body so
            // the page learns nothing it could use to probe the list.
            send(task, status: 403, "forbidden by the workspace allowlist")
            return
        case .allow(let m, let p):
            forwardMethod = m
            path = p
        }
        guard let body = Self.body(of: request) else {
            send(task, status: 413, "request body too large")
            return
        }

        let key = ObjectIdentifier(task)
        let cancel = CancelToken()
        live[key] = cancel
        let client = self.client, tokens = self.tokens

        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            var headSent = false
            var failure: Error?
            do {
                let token = try tokens.token()
                let req = HTTPRequest(method: forwardMethod, path: path, token: token,
                                      body: forwardMethod == "POST" ? body : nil)
                try client.perform(req, cancel: cancel, onHead: { status, headers in
                    headSent = true
                    let fields = WorkspaceProxy.responseHeaders(from: headers)
                    DispatchQueue.main.async {
                        guard self?.isLive(key) == true,
                              let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: fields)
                        else { return }
                        task.didReceive(resp)
                    }
                }, onBody: { data in
                    // Chunked/NDJSON bodies arrive here as the daemon writes
                    // them, and go to WebKit one run at a time.
                    DispatchQueue.main.async {
                        guard self?.isLive(key) == true else { return }
                        task.didReceive(data)
                    }
                })
            } catch {
                failure = error
            }
            DispatchQueue.main.async {
                guard let self, self.live.removeValue(forKey: key) != nil else { return } // stopped
                if let failure {
                    if headSent {
                        task.didFailWithError(failure)
                    } else {
                        // The daemon never answered (not running, no token):
                        // a plain-text 502 the page can show.
                        let text = (failure as? LocalizedError)?.errorDescription ?? "water daemon unreachable"
                        self.send(task, status: 502, text)
                    }
                } else {
                    task.didFinish()
                }
            }
        }
    }

    func webView(_ webView: WKWebView, stop task: WKURLSchemeTask) {
        // The page navigated away or aborted the fetch (e.g. the thread
        // view's Stop). Closing the socket also cancels the turn daemon-side.
        live.removeValue(forKey: ObjectIdentifier(task))?.cancel()
    }

    private func isLive(_ key: ObjectIdentifier) -> Bool { live[key] != nil }

    /// A response the proxy writes itself (denied, malformed, daemon
    /// unreachable): main thread, task not (or no longer) in `live`.
    private func send(_ task: WKURLSchemeTask, status: Int, _ text: String) {
        let url = task.request.url ?? WorkspaceAllowlist.startURL
        guard let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1",
                                         headerFields: WorkspaceProxy.localErrorHeaders()) else { return }
        task.didReceive(resp)
        task.didReceive(Data(text.utf8))
        task.didFinish()
    }

    /// The request body, from `httpBody` or (when WebKit hands a stream)
    /// `httpBodyStream`, capped at `WorkspaceProxy.maxRequestBody`. Nil
    /// means it was over the cap.
    private static func body(of request: URLRequest) -> Data? {
        let limit = WorkspaceProxy.maxRequestBody
        if let b = request.httpBody { return b.count <= limit ? b : nil }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var out = Data()
        var buf = [UInt8](repeating: 0, count: 16 * 1024)
        while true {
            let n = stream.read(&buf, maxLength: buf.count)
            if n <= 0 { break }
            out.append(buf, count: n)
            if out.count > limit { return nil }
        }
        return out
    }
}

/// The workspace window: one NSWindow + WKWebView, created on first use and
/// kept (hidden, not destroyed) when closed so an open thread survives.
final class WorkspaceWindowController: NSObject, WKNavigationDelegate, WKUIDelegate {
    /// The bar's hold-to-talk mic (V-ui2): pressed, for this thread
    /// (`{type: "mic-down", thread}`), and released (`{type: "mic-up"}`).
    var onMicDown: ((String) -> Void)?
    var onMicUp: (() -> Void)?

    private let schemeHandler: WorkspaceSchemeHandler
    private var window: NSWindow?
    private var webView: WKWebView?
    /// True once the page has finished loading (so `window.water` exists).
    private var pageLoaded = false
    /// An `open(view:id:)` waiting for the page to finish loading.
    private var pendingOpen: (view: String, id: String)?

    init(client: UnixSocketClient, tokens: TokenProvider) {
        schemeHandler = WorkspaceSchemeHandler(client: client, tokens: tokens)
    }

    var isVisible: Bool { window?.isVisible == true }

    func show() {
        if window == nil { build() }
        NSApp.activate(ignoringOtherApps: true)
        window?.makeKeyAndOrderFront(nil)
    }

    /// Shows the window at one record: `window.water.open(view, id)` in the
    /// page (app.js), with both values passed as `callAsyncJavaScript`
    /// arguments, never spliced into script text. Used by the Activity
    /// HUD's Edit (`approvals`, envelope id) and a tapped notification
    /// (`threads`, thread id; or a record's own view). A nil id opens the
    /// view's list. An unknown view or a non-id just shows the window.
    func open(view: String, id: String?) {
        show()
        guard WorkspaceAllowlist.isOpenTarget(view: view, id: id) else { return }
        let id = id ?? ""
        if pageLoaded, let webView {
            Self.callOpen(webView, view: view, id: id)
        } else {
            pendingOpen = (view, id)
        }
    }

    private static func callOpen(_ webView: WKWebView, view: String, id: String) {
        webView.callAsyncJavaScript("if (window.water) { window.water.open(view, id); }",
                                    arguments: ["view": view, "id": id], in: nil, in: .page) { _ in }
    }

    /// Re-renders the page's current view (`window.water.refresh()`), e.g.
    /// after a held-mic voice turn stored its messages in the open thread.
    /// A no-op until the page has loaded; it never shows the window.
    func refresh() {
        guard pageLoaded, let webView else { return }
        webView.callAsyncJavaScript("if (window.water) { window.water.refresh(); }",
                                    arguments: [:], in: nil, in: .page) { _ in }
    }

    /// Hotkey: bring it forward, or hide it when it is already in front.
    func toggle() {
        if let window, window.isVisible, window.isKeyWindow { window.orderOut(nil) } else { show() }
    }

    private func build() {
        let config = WKWebViewConfiguration()
        config.setURLSchemeHandler(schemeHandler, forURLScheme: WorkspaceAllowlist.scheme)
        // Nothing survives the app: no cookies, cache or storage on disk.
        config.websiteDataStore = .nonPersistent()
        config.preferences.javaScriptCanOpenWindowsAutomatically = false
        config.preferences.isElementFullscreenEnabled = false
        config.allowsAirPlayForMediaPlayback = false
        config.mediaTypesRequiringUserActionForPlayback = .all
        config.suppressesIncrementalRendering = false
        config.userContentController.add(ScriptMessageProxy(self), name: WorkspaceMessage.handlerName)

        let wv = WKWebView(frame: NSRect(x: 0, y: 0, width: 1100, height: 720), configuration: config)
        wv.navigationDelegate = self
        wv.uiDelegate = self
        wv.allowsBackForwardNavigationGestures = false
        wv.allowsLinkPreview = false
        wv.allowsMagnification = true
        wv.autoresizingMask = [.width, .height]

        let w = WorkspaceWindow(contentRect: NSRect(x: 0, y: 0, width: 1100, height: 720),
                                styleMask: [.titled, .closable, .miniaturizable, .resizable],
                                backing: .buffered, defer: false)
        w.title = "Water Workspace"
        w.isReleasedWhenClosed = false
        w.contentMinSize = NSSize(width: 640, height: 420)
        w.contentView = wv
        w.setFrameAutosaveName("WaterWorkspace")
        if !w.setFrameUsingName("WaterWorkspace") { w.center() }
        window = w
        webView = wv
        wv.load(URLRequest(url: WorkspaceAllowlist.startURL))
    }

    // MARK: navigation policy

    /// Only the daemon's own origin loads inside; every other URL — a link
    /// in attacker-controlled text, a dropped file, a redirect — is
    /// cancelled, and never handed to NSWorkspace either.
    static func isInside(_ url: URL?) -> Bool {
        guard let url else { return false }
        return url.scheme?.lowercased() == WorkspaceAllowlist.scheme && url.host == WorkspaceAllowlist.host
            && url.user == nil && url.password == nil && url.port == nil
    }

    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        let ok = action.targetFrame != nil && !action.shouldPerformDownload && Self.isInside(action.request.url)
        decisionHandler(ok ? .allow : .cancel)
    }

    func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse,
                 decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        decisionHandler(response.canShowMIMEType && Self.isInside(response.response.url) ? .allow : .cancel)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        pageLoaded = false
        webView.load(URLRequest(url: WorkspaceAllowlist.startURL))
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        pageLoaded = true
        if let p = pendingOpen {
            pendingOpen = nil
            Self.callOpen(webView, view: p.view, id: p.id)
        }
    }

    // MARK: UI delegate: no windows, no panels

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        nil // no JavaScript-opened windows, and target=_blank links go nowhere
    }

    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        completionHandler(nil) // no file access
    }

    // MARK: script messages

    fileprivate func receive(_ message: WKScriptMessage) {
        guard message.name == WorkspaceMessage.handlerName, message.frameInfo.isMainFrame else { return }
        let origin = message.frameInfo.securityOrigin
        guard origin.protocol == WorkspaceAllowlist.scheme, origin.host == WorkspaceAllowlist.host else { return }
        switch WorkspaceMessage.parse(message.body) {
        case .micDown(let thread)?: onMicDown?(thread)
        case .micUp?: onMicUp?()
        case nil: break // anything else, a malformed thread id included, is ignored
        }
    }
}

/// WKUserContentController retains its handlers strongly; this breaks the
/// cycle back to the window controller.
private final class ScriptMessageProxy: NSObject, WKScriptMessageHandler {
    private weak var target: WorkspaceWindowController?
    init(_ target: WorkspaceWindowController) { self.target = target }
    func userContentController(_ c: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.receive(message)
    }
}

/// Water is an accessory app with no main menu, so the standard editing
/// shortcuts have nothing to route through; send them to the first
/// responder directly so ⌘C/⌘V/⌘X/⌘A/⌘Z work in the page's text fields.
private final class WorkspaceWindow: NSWindow {
    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        let mods = event.modifierFlags.intersection([.command, .shift, .option, .control])
        let action: Selector?
        switch (mods, event.charactersIgnoringModifiers) {
        case ([.command], "c"): action = #selector(NSText.copy(_:))
        case ([.command], "v"): action = #selector(NSText.paste(_:))
        case ([.command], "x"): action = #selector(NSText.cut(_:))
        case ([.command], "a"): action = #selector(NSText.selectAll(_:))
        case ([.command], "z"): action = Selector(("undo:"))
        case ([.command, .shift], "z"), ([.command, .shift], "Z"): action = Selector(("redo:"))
        case ([.command], "w"): action = #selector(NSWindow.performClose(_:))
        default: action = nil
        }
        if let action, NSApp.sendAction(action, to: nil, from: self) { return true }
        return super.performKeyEquivalent(with: event)
    }
}
