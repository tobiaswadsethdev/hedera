@preconcurrency import AVFoundation
import HederaKit
import SwiftUI

/// Scan (or paste) a `hedera://pair` code, confirm, then pair.
struct PairView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let initialURL: String?
    let onPaired: (MachineClient) -> Void

    @State private var parsed: PairingURL?
    @State private var pasted = ""
    @State private var deviceName = AppModel.defaultDeviceName
    @State private var working = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                if let p = parsed {
                    confirm(p)
                } else {
                    scan
                }
                if let error {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle(parsed == nil ? "Pair a Machine" : "Pair \(parsed!.name)")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .onAppear {
                if let initialURL { accept(initialURL) }
            }
        }
    }

    private var scan: some View {
        Group {
            Section {
                QRScannerView { code in accept(code) }
                    .frame(height: 300)
                    .listRowInsets(EdgeInsets())
            } footer: {
                Text("Run `hedera pair` on the computer and point the camera at the code.")
            }
            Section("Or paste the link") {
                TextField("hedera://pair?…", text: $pasted, axis: .vertical)
                    .font(.footnote.monospaced())
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                Button("Continue") { accept(pasted) }
                    .disabled(pasted.isEmpty)
            }
        }
    }

    @ViewBuilder
    private func confirm(_ p: PairingURL) -> some View {
        Section {
            LabeledContent("Machine", value: p.name)
            LabeledContent("Address", value: p.hosts.joined(separator: ", "))
            LabeledContent("Port", value: p.port)
            LabeledContent("Key") {
                Text(p.fingerprint.dropFirst("sha256:".count).prefix(16) + "…")
                    .font(.footnote.monospaced())
            }
        } footer: {
            Text("Hedera connects only if the machine presents exactly this key. Check the name is the computer you meant to pair.")
        }
        Section("This phone's name on the machine") {
            TextField("Device name", text: $deviceName)
        }
        Section {
            Button {
                Task { await pair(p) }
            } label: {
                HStack {
                    Text("Pair")
                    if working {
                        Spacer()
                        ProgressView()
                    }
                }
            }
            .disabled(working || deviceName.trimmingCharacters(in: .whitespaces).isEmpty)
            Button("Scan Another Code") {
                parsed = nil
                error = nil
            }
            .disabled(working)
        }
    }

    private func accept(_ raw: String) {
        do {
            parsed = try PairingURL(raw)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func pair(_ p: PairingURL) async {
        working = true
        defer { working = false }
        do {
            let record = try await Pairing.pair(p, deviceName: deviceName.trimmingCharacters(in: .whitespaces))
            let client = try model.add(record)
            onPaired(client)
        } catch {
            self.error = error.localizedDescription
        }
    }
}

/// A camera preview that reports the first QR code it sees.
struct QRScannerView: UIViewControllerRepresentable {
    let onCode: (String) -> Void

    func makeUIViewController(context: Context) -> ScannerController {
        let c = ScannerController()
        c.onCode = onCode
        return c
    }

    func updateUIViewController(_ controller: ScannerController, context: Context) {
        controller.onCode = onCode
    }

    final class ScannerController: UIViewController, @preconcurrency AVCaptureMetadataOutputObjectsDelegate {
        var onCode: ((String) -> Void)?
        private let session = AVCaptureSession()
        private var preview: AVCaptureVideoPreviewLayer?
        private var lastCode: String?
        private let message = UILabel()

        override func viewDidLoad() {
            super.viewDidLoad()
            view.backgroundColor = .black
            message.textColor = .white.withAlphaComponent(0.8)
            message.font = .preferredFont(forTextStyle: .footnote)
            message.numberOfLines = 0
            message.textAlignment = .center
            message.translatesAutoresizingMaskIntoConstraints = false
            view.addSubview(message)
            NSLayoutConstraint.activate([
                message.centerYAnchor.constraint(equalTo: view.centerYAnchor),
                message.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 24),
                message.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -24),
            ])
            switch AVCaptureDevice.authorizationStatus(for: .video) {
            case .authorized:
                configure()
            case .notDetermined:
                AVCaptureDevice.requestAccess(for: .video) { ok in
                    Task { @MainActor in ok ? self.configure() : self.show("Camera access is off. Paste the link instead.") }
                }
            default:
                show("Camera access is off. Allow it in Settings, or paste the link instead.")
            }
        }

        private func show(_ text: String) {
            message.text = text
        }

        private func configure() {
            guard let device = AVCaptureDevice.default(for: .video),
                  let input = try? AVCaptureDeviceInput(device: device),
                  session.canAddInput(input)
            else {
                show("No camera available. Paste the link instead.")
                return
            }
            session.addInput(input)
            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output) else { return }
            session.addOutput(output)
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]
            let layer = AVCaptureVideoPreviewLayer(session: session)
            layer.videoGravity = .resizeAspectFill
            view.layer.insertSublayer(layer, at: 0)
            preview = layer
            nonisolated(unsafe) let session = self.session
            DispatchQueue.global(qos: .userInitiated).async { session.startRunning() }
        }

        override func viewDidLayoutSubviews() {
            super.viewDidLayoutSubviews()
            preview?.frame = view.bounds
        }

        override func viewWillDisappear(_ animated: Bool) {
            super.viewWillDisappear(animated)
            nonisolated(unsafe) let session = self.session
            DispatchQueue.global(qos: .userInitiated).async { session.stopRunning() }
        }

        func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput objects: [AVMetadataObject],
                            from connection: AVCaptureConnection) {
            guard let code = (objects.first as? AVMetadataMachineReadableCodeObject)?.stringValue,
                  code != lastCode
            else { return }
            lastCode = code
            UINotificationFeedbackGenerator().notificationOccurred(.success)
            onCode?(code)
        }
    }
}
