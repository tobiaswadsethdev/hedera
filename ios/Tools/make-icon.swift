// Draws the app icon, an ivy (Hedera helix) leaf, at 1024×1024.
// Run from ios/: swift Tools/make-icon.swift Hedera/Assets.xcassets/AppIcon.appiconset/icon-1024.png

import AppKit
import CoreGraphics
import Foundation

let size = 1024.0
let out = CommandLine.arguments.dropFirst().first ?? "icon-1024.png"

let space = CGColorSpace(name: CGColorSpace.sRGB)!
let ctx = CGContext(data: nil, width: Int(size), height: Int(size), bitsPerComponent: 8, bytesPerRow: 0,
                    space: space, bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!

func rgb(_ hex: UInt32, _ a: CGFloat = 1) -> CGColor {
    CGColor(srgbRed: CGFloat(hex >> 16 & 0xFF) / 255, green: CGFloat(hex >> 8 & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255, alpha: a)
}

// Background: deep ink green, lighter toward the top.
let bg = CGGradient(colorsSpace: space, colors: [rgb(0x0E2A22), rgb(0x061611)] as CFArray, locations: [0, 1])!
ctx.drawLinearGradient(bg, start: CGPoint(x: 0, y: size), end: CGPoint(x: 0, y: 0), options: [])

// The leaf outline in polar form around its centre: five pointed lobes (the
// classic ivy shape) and a heart-shaped notch where the stem joins.
let centre = CGPoint(x: size / 2, y: size * 0.50)
let lobes: [(angle: Double, length: Double, width: Double)] = [
    (90, 1.00, 0.62),   // top
    (22, 0.84, 0.58),   // right
    (158, 0.84, 0.58),  // left
    (-38, 0.68, 0.50),  // lower right
    (218, 0.68, 0.50),  // lower left
]
func radius(_ theta: Double) -> Double {
    var r = 0.50
    for l in lobes {
        var d = abs(theta - l.angle * .pi / 180).truncatingRemainder(dividingBy: 2 * .pi)
        if d > .pi { d = 2 * .pi - d }
        r = max(r, 0.50 + (l.length - 0.50) * max(0, 1 - pow(d / l.width, 0.9)))
    }
    // The heart-shaped notch at the bottom (270°).
    var d = abs(theta - 1.5 * .pi)
    if d > .pi { d = 2 * .pi - d }
    r -= 0.30 * max(0, 1 - pow(d / 0.45, 0.7))
    return r * size * 0.38
}

let leaf = CGMutablePath()
let steps = 720
for i in 0...steps {
    let t = Double(i) / Double(steps) * 2 * .pi
    let r = radius(t)
    let p = CGPoint(x: centre.x + r * cos(t), y: centre.y + r * sin(t))
    i == 0 ? leaf.move(to: p) : leaf.addLine(to: p)
}
leaf.closeSubpath()

// Stem.
let stemBase = CGPoint(x: centre.x, y: centre.y - radius(1.5 * .pi))
ctx.setStrokeColor(rgb(0x5E8F3E))
ctx.setLineWidth(size * 0.028)
ctx.setLineCap(.round)
ctx.move(to: stemBase)
ctx.addCurve(to: CGPoint(x: centre.x - size * 0.09, y: size * 0.17),
             control1: CGPoint(x: centre.x + size * 0.01, y: size * 0.26),
             control2: CGPoint(x: centre.x - size * 0.03, y: size * 0.20))
ctx.strokePath()

// Leaf body with a soft shadow and a gradient fill.
ctx.saveGState()
ctx.setShadow(offset: CGSize(width: 0, height: -size * 0.012), blur: size * 0.04, color: rgb(0x000000, 0.45))
ctx.addPath(leaf)
ctx.setFillColor(rgb(0x3E7A2E))
ctx.fillPath()
ctx.restoreGState()

ctx.saveGState()
ctx.addPath(leaf)
ctx.clip()
let fill = CGGradient(colorsSpace: space, colors: [rgb(0x8CC152), rgb(0x3F7F2C), rgb(0x24521D)] as CFArray,
                      locations: [0, 0.55, 1])!
ctx.drawRadialGradient(fill, startCenter: CGPoint(x: centre.x - size * 0.08, y: centre.y + size * 0.16),
                       startRadius: 0, endCenter: centre, endRadius: size * 0.45, options: [.drawsAfterEndLocation])

// Pale veins from the stem joint out to each lobe tip.
ctx.setStrokeColor(rgb(0xD9EFC2, 0.85))
ctx.setLineCap(.round)
let joint = CGPoint(x: centre.x, y: centre.y - size * 0.07)
for (i, l) in lobes.enumerated() {
    let t = l.angle * .pi / 180
    let tip = CGPoint(x: centre.x + radius(t) * 0.86 * cos(t), y: centre.y + radius(t) * 0.86 * sin(t))
    ctx.setLineWidth(size * (i == 0 ? 0.016 : 0.012))
    ctx.move(to: joint)
    ctx.addQuadCurve(to: tip, control: CGPoint(x: (joint.x + tip.x) / 2 + (tip.y - joint.y) * 0.06,
                                               y: (joint.y + tip.y) / 2 - (tip.x - joint.x) * 0.06))
    ctx.strokePath()
}
ctx.restoreGState()

let image = ctx.makeImage()!
let rep = NSBitmapImageRep(cgImage: image)
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: out))
print("wrote \(out)")
