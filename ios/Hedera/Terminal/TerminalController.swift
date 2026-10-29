import HederaKit
import SwiftTerm
import UIKit

/// Connects a SwiftTerm view to a session: output from the attachment is fed
/// to the terminal, keystrokes and size changes go back to the agent.
@MainActor
final class TerminalController: NSObject, TerminalSink {
    let view: TerminalView
    private(set) var attachment: TerminalAttachment?
    private let machine: MachineClient
    private let sessionID: String
    var onEnded: ((String) -> Void)?
    /// The terminal gained (true) or lost focus, which is when the keyboard
    /// comes and goes.
    var onFocusChange: ((Bool) -> Void)?
    /// False while the app is locked: keystrokes are dropped, not sent.
    var inputEnabled = true

    init(machine: MachineClient, sessionID: String) {
        self.machine = machine
        self.sessionID = sessionID
        let v = FocusReportingTerminalView(frame: .zero)
        view = v
        super.init()
        v.onFocusChange = { [weak self] in self?.onFocusChange?($0) }
        view.terminalDelegate = self
        view.font = UIFont.monospacedSystemFont(ofSize: 13, weight: .regular)
        view.nativeBackgroundColor = .black
        view.nativeForegroundColor = UIColor(white: 0.92, alpha: 1)
        view.keyboardAppearance = .dark
        view.optionAsMetaKey = true
        // SwiftTerm's default input accessory already has Esc, Ctrl (sticky),
        // Tab, ~, |, /, -, the arrow keys and F1–F10.
    }

    /// Attaches with the terminal's current size. Called once the view has
    /// been laid out, so the replay is rendered at the right width.
    func start() {
        guard attachment == nil else { return }
        let t = view.getTerminal()
        let a = machine.openTerminal(sessionID: sessionID, size: TermSize(cols: t.cols, rows: t.rows), sink: self)
        a.onEnded = { [weak self] reason in self?.onEnded?(reason) }
        attachment = a
    }

    func stop() {
        attachment?.close()
        attachment = nil
    }

    // MARK: TerminalSink

    func terminalReset() {
        // RIS: full reset, as the spec suggests for a truncated replay.
        view.feed(text: "\u{1b}c")
    }

    func terminalWrite(_ data: Data) {
        view.feed(byteArray: ArraySlice([UInt8](data)))
    }
}

/// SwiftTerm's view, saying when it becomes or stops being first responder.
/// Keyboard notifications aren't enough: with a hardware keyboard only the
/// accessory bar shows, and they can fire before anyone is listening.
final class FocusReportingTerminalView: TerminalView {
    var onFocusChange: ((Bool) -> Void)?

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok { onFocusChange?(true) }
        return ok
    }

    override func resignFirstResponder() -> Bool {
        let ok = super.resignFirstResponder()
        if ok { onFocusChange?(false) }
        return ok
    }
}

extension TerminalController: @preconcurrency TerminalViewDelegate {
    func send(source: TerminalView, data: ArraySlice<UInt8>) {
        guard inputEnabled else { return }
        attachment?.send(Data(data))
    }

    func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        if let attachment {
            attachment.resize(cols: newCols, rows: newRows)
        } else {
            start()
        }
    }

    func setTerminalTitle(source: TerminalView, title: String) {}

    func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}

    func scrolled(source: TerminalView, position: Double) {}

    func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
        guard let url = URL(string: link), ["http", "https"].contains(url.scheme) else { return }
        UIApplication.shared.open(url)
    }

    func bell(source: TerminalView) {
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
    }

    func clipboardCopy(source: TerminalView, content: Data) {
        UIPasteboard.general.string = String(decoding: content, as: UTF8.self)
    }

    func clipboardRead(source: TerminalView) -> Data? {
        // OSC 52 reads would let any program on the machine read the phone's
        // clipboard; refuse.
        nil
    }

    func iTermContent(source: TerminalView, content: ArraySlice<UInt8>) {}

    func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
}
