import HederaKit
import SwiftUI

/// A Claude session as a conversation, built from its transcript. The PTY is
/// still the session: what is sent here is typed into it.
struct ChatView: View {
    let machine: MachineClient
    let sessionID: String
    let feed: TranscriptFeed
    /// Switches the session screen to the terminal.
    let showTerminal: () -> Void
    @State private var draft = ""
    @State private var atBottom = true
    @State private var scrolledToLatest = false
    /// Bumped to scroll to the latest entry from outside the scroll view.
    @State private var jumps = 0
    @State private var sending = false
    @State private var error: String?
    @FocusState private var composing: Bool

    private var session: Session? { machine.session(sessionID) }
    private var claude: ClaudeState? { session?.claude }
    private var working: Bool { claude?.status == SessionStatus.working }
    private var items: [ChatItem] { ChatItem.fold(feed.entries) }

    var body: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ZStack(alignment: .bottomTrailing) {
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 12) {
                            if feed.hasOlder {
                                ProgressView()
                                    .frame(maxWidth: .infinity)
                                    .onAppear { loadOlder(proxy) }
                            }
                            if feed.entries.isEmpty {
                                emptyState
                            }
                            ForEach(items) { item in
                                row(item).id(item.id)
                            }
                            Color.clear.frame(height: 1).id("bottom")
                                .onAppear { atBottom = true }
                                .onDisappear { atBottom = false }
                        }
                        .padding(.horizontal, 14)
                        .padding(.vertical, 12)
                    }
                    .scrollDismissesKeyboard(.interactively)
                    .defaultScrollAnchor(.bottom)
                    .onChange(of: feed.entries.last?.seq) {
                        if !scrolledToLatest {
                            // The first entries arrive after the view is drawn.
                            scrolledToLatest = true
                            proxy.scrollTo("bottom", anchor: .bottom)
                        } else if atBottom {
                            withAnimation { proxy.scrollTo("bottom", anchor: .bottom) }
                        }
                    }
                    .onChange(of: jumps) {
                        withAnimation { proxy.scrollTo("bottom", anchor: .bottom) }
                    }
                    .onAppear {
                        if !feed.entries.isEmpty {
                            scrolledToLatest = true
                            proxy.scrollTo("bottom", anchor: .bottom)
                        }
                    }
                    if !atBottom {
                        Button {
                            withAnimation { proxy.scrollTo("bottom", anchor: .bottom) }
                        } label: {
                            Image(systemName: "arrow.down")
                                .font(.headline)
                                .padding(10)
                                .background(.ultraThinMaterial, in: Circle())
                        }
                        .padding(14)
                    }
                }
            }
            if let needs = claude?.needsInput, claude?.status == SessionStatus.needsInput {
                needsInputBanner(needs)
            }
            composer
        }
        .background(Color.black)
        // Dark like the terminal it sits beside, whatever the system setting.
        .environment(\.colorScheme, .dark)
        .alert("Couldn't send", isPresented: .constant(error != nil)) {
            Button("OK") { error = nil }
        } message: {
            Text(error ?? "")
        }
    }

    @ViewBuilder
    private var emptyState: some View {
        VStack(spacing: 8) {
            if feed.loaded || feed.unavailable {
                Text("No messages yet").font(.headline)
                Text("Send Claude a message below.")
            } else {
                ProgressView()
            }
        }
        .font(.subheadline)
        .foregroundStyle(.secondary)
        .multilineTextAlignment(.center)
        .frame(maxWidth: .infinity)
        .padding(.top, 60)
    }

    @ViewBuilder
    private func row(_ item: ChatItem) -> some View {
        switch item {
        case let .prompt(_, text, images):
            HStack {
                Spacer(minLength: 40)
                VStack(alignment: .trailing, spacing: 4) {
                    if images > 0 {
                        Label("\(images) image\(images == 1 ? "" : "s")", systemImage: "photo")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                    if !text.isEmpty {
                        Text(text)
                            .textSelection(.enabled)
                            .padding(.horizontal, 12)
                            .padding(.vertical, 8)
                            .background(Color.accentColor.opacity(0.35), in: RoundedRectangle(cornerRadius: 16))
                    }
                }
            }
        case let .text(_, text):
            MarkdownText(text: text)
        case let .thinking(_, text):
            ThinkingView(text: text)
        case let .tool(_, call):
            ToolCard(call: call, cwd: session?.cwd ?? "", working: working)
        case let .turnEnd(_, ms):
            Text("Worked for \(Self.duration(ms))")
                .font(.caption)
                .foregroundStyle(.tertiary)
                .frame(maxWidth: .infinity)
        case let .divider(_, reason):
            HStack {
                VStack { Divider() }
                Text(Self.dividerText(reason)).font(.caption).foregroundStyle(.secondary).fixedSize()
                VStack { Divider() }
            }
        case let .notice(_, text, isError):
            Text(text)
                .font(.caption)
                .foregroundStyle(isError ? Color.red.opacity(0.85) : .secondary)
                .textSelection(.enabled)
        }
    }

    private func needsInputBanner(_ needs: NeedsInput) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "hand.raised.fill").foregroundStyle(.orange)
            VStack(alignment: .leading, spacing: 6) {
                Text(needs.message).font(.subheadline)
                Button("Answer in the Terminal", action: showTerminal)
                    .font(.subheadline.weight(.semibold))
            }
            Spacer(minLength: 0)
        }
        .padding(12)
        .background(Color.orange.opacity(0.15))
    }

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 8) {
            TextField("Message Claude", text: $draft, axis: .vertical)
                .lineLimit(1...6)
                .focused($composing)
                .onChange(of: composing) {
                    // The keyboard pushes the latest entry out of view; bring it back once it's up.
                    guard composing else { return }
                    Task {
                        try? await Task.sleep(for: .milliseconds(350))
                        jumps += 1
                    }
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(Color.white.opacity(0.08), in: RoundedRectangle(cornerRadius: 18))
            if working && draft.isEmpty {
                Button(action: stop) {
                    Image(systemName: "stop.circle.fill").font(.system(size: 30))
                }
                .tint(.red)
                .accessibilityLabel("Stop Claude")
            } else {
                Button(action: send) {
                    Image(systemName: "arrow.up.circle.fill").font(.system(size: 30))
                }
                .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || sending)
                .accessibilityLabel("Send")
            }
        }
        .disabled(machine.state != .online || session?.exit != nil)
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .background(.bar)
    }

    private func send() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        sending = true
        jumps += 1
        Task {
            defer { sending = false }
            do {
                try await machine.sendInput(sessionID: sessionID, text: text, submit: true)
                draft = ""
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    /// Esc, which is how Claude Code is interrupted.
    private func stop() {
        Task { try? await machine.sendInput(sessionID: sessionID, text: "\u{1b}", submit: false) }
    }

    private func loadOlder(_ proxy: ScrollViewProxy) {
        let anchor = items.first?.id
        Task {
            await feed.loadOlder()
            // Keep what was at the top in place instead of jumping.
            if let anchor { proxy.scrollTo(anchor, anchor: .top) }
        }
    }

    nonisolated static func duration(_ ms: Int64) -> String {
        let s = Int(ms / 1000)
        if s < 60 { return "\(s)s" }
        if s < 3600 { return "\(s / 60)m \(s % 60)s" }
        return "\(s / 3600)h \(s % 3600 / 60)m"
    }

    nonisolated static func dividerText(_ reason: String) -> String {
        switch reason {
        case "compact": "Conversation compacted"
        case "clear": "Conversation cleared"
        case "resume": "Resumed a conversation"
        case "fork": "Forked the conversation"
        default: reason
        }
    }
}

struct ThinkingView: View {
    let text: String
    @State private var open = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                withAnimation(.snappy) { open.toggle() }
            } label: {
                Label("Thinking", systemImage: open ? "chevron.down" : "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            .buttonStyle(.plain)
            if open {
                Text(text)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
        }
    }
}
