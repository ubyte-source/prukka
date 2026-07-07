// icongen renders the Prukka brand mark — the teal winged helmet from
// assets/brand/prukka.svg — as a 1024px PNG on the macOS icon card;
// build.sh turns it into the app's .icns so no binary asset lives in the
// repository. Geometry is the SVG's 256-unit viewBox, hand-translated.

import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let size = 1024

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: icongen <out.png>\n".utf8))
    exit(1)
}

let ctx = CGContext(
    data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: 0,
    space: CGColorSpace(name: CGColorSpace.sRGB)!,
    bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!

let teal = CGColor(red: 0x0F / 255.0, green: 0x76 / 255.0, blue: 0x6E / 255.0, alpha: 1)
let white = CGColor(red: 1, green: 1, blue: 1, alpha: 1)

// The macOS icon card: a white rounded rectangle on the Apple grid
// (content ~824pt of 1024 with even margins).
let cardInset = CGFloat(100)
let card = CGRect(x: cardInset, y: cardInset,
                  width: CGFloat(size) - 2 * cardInset, height: CGFloat(size) - 2 * cardInset)
ctx.addPath(CGPath(roundedRect: card, cornerWidth: 185, cornerHeight: 185, transform: nil))
ctx.setFillColor(white)
ctx.fillPath()

// Map the SVG's 256-unit y-down viewBox into the card, y-up.
let content = card.width * 0.86
let unit = content / 256
let origin = CGPoint(x: (CGFloat(size) - content) / 2, y: (CGFloat(size) - content) / 2)

func pt(_ x: CGFloat, _ y: CGFloat, dx: CGFloat = 0, dy: CGFloat = 0) -> CGPoint {
    CGPoint(x: origin.x + (x + dx) * unit, y: origin.y + (256 - y - dy) * unit)
}

func strokeWidth(_ w: CGFloat) { ctx.setLineWidth(w * unit) }

ctx.setLineCap(.round)
ctx.setLineJoin(.round)
ctx.setStrokeColor(teal)

// wing draws the feathered wing path, offset by (dx, dy) for the far wing.
func wing(dx: CGFloat, dy: CGFloat) {
    let p = CGMutablePath()
    p.move(to: pt(120, 178, dx: dx, dy: dy))
    p.addCurve(to: pt(62, 149, dx: dx, dy: dy),
               control1: pt(100, 163, dx: dx, dy: dy), control2: pt(74, 159, dx: dx, dy: dy))
    p.addCurve(to: pt(53, 129, dx: dx, dy: dy),
               control1: pt(55, 143, dx: dx, dy: dy), control2: pt(52, 136, dx: dx, dy: dy))
    p.addCurve(to: pt(31, 109, dx: dx, dy: dy),
               control1: pt(43, 126, dx: dx, dy: dy), control2: pt(34, 120, dx: dx, dy: dy))
    p.addCurve(to: pt(11, 72, dx: dx, dy: dy),
               control1: pt(18, 101, dx: dx, dy: dy), control2: pt(8, 86, dx: dx, dy: dy))
    p.addCurve(to: pt(40, 66, dx: dx, dy: dy),
               control1: pt(14, 57, dx: dx, dy: dy), control2: pt(29, 55, dx: dx, dy: dy))
    p.addCurve(to: pt(149, 111, dx: dx, dy: dy),
               control1: pt(64, 97, dx: dx, dy: dy), control2: pt(108, 107, dx: dx, dy: dy))
    p.addCurve(to: pt(188, 141, dx: dx, dy: dy),
               control1: pt(170, 113, dx: dx, dy: dy), control2: pt(184, 124, dx: dx, dy: dy))
    p.addCurve(to: pt(164, 183, dx: dx, dy: dy),
               control1: pt(192, 158, dx: dx, dy: dy), control2: pt(182, 174, dx: dx, dy: dy))
    p.addCurve(to: pt(120, 178, dx: dx, dy: dy),
               control1: pt(147, 191, dx: dx, dy: dy), control2: pt(131, 188, dx: dx, dy: dy))
    p.closeSubpath()

    ctx.addPath(p)
    ctx.setFillColor(white)
    strokeWidth(7)
    ctx.drawPath(using: .fillStroke)

    let feathers: [((CGFloat, CGFloat), (CGFloat, CGFloat), (CGFloat, CGFloat), (CGFloat, CGFloat))] = [
        ((53, 129), (68, 137), (88, 143), (109, 146)),
        ((62, 149), (78, 157), (98, 162), (119, 164)),
        ((31, 109), (48, 122), (71, 131), (95, 136)),
    ]
    for f in feathers {
        let q = CGMutablePath()
        q.move(to: pt(f.0.0, f.0.1, dx: dx, dy: dy))
        q.addCurve(to: pt(f.3.0, f.3.1, dx: dx, dy: dy),
                   control1: pt(f.1.0, f.1.1, dx: dx, dy: dy),
                   control2: pt(f.2.0, f.2.1, dx: dx, dy: dy))
        ctx.addPath(q)
        strokeWidth(7)
        ctx.strokePath()
    }
}

// Far wing, behind the helmet (the SVG's translate(18,-16)).
wing(dx: 18, dy: -16)

// Helmet body, teal.
let helmet = CGMutablePath()
helmet.move(to: pt(107, 151))
helmet.addCurve(to: pt(204, 170), control1: pt(133, 132), control2: pt(178, 139))
helmet.addCurve(to: pt(194, 210), control1: pt(218, 187), control2: pt(214, 202))
helmet.addCurve(to: pt(94, 203), control1: pt(168, 220), control2: pt(113, 219))
helmet.addCurve(to: pt(92, 165), control1: pt(82, 193), control2: pt(82, 177))
helmet.addCurve(to: pt(107, 151), control1: pt(96, 159), control2: pt(101, 155))
helmet.closeSubpath()
ctx.addPath(helmet)
ctx.setFillColor(teal)
strokeWidth(7)
ctx.drawPath(using: .fillStroke)

// Brim highlight along the helmet's lower edge (the SVG polyline, smoothed).
let brim = CGMutablePath()
brim.move(to: pt(121, 205))
brim.addCurve(to: pt(190, 202), control1: pt(143, 208), control2: pt(172, 208))
brim.addCurve(to: pt(203, 189), control1: pt(198, 198), control2: pt(203, 194))
brim.addCurve(to: pt(193, 171), control1: pt(203, 184), control2: pt(199, 177))
ctx.addPath(brim)
ctx.setStrokeColor(white)
strokeWidth(6)
ctx.strokePath()
ctx.setStrokeColor(teal)

// Near wing, over the helmet.
wing(dx: 0, dy: 0)

// The rivet: white ring, teal center.
let ring = CGRect(x: pt(116 - 22, 174).x, y: pt(116, 174 + 22).y,
                  width: 44 * unit, height: 44 * unit)
ctx.addEllipse(in: ring)
ctx.setFillColor(white)
strokeWidth(7)
ctx.drawPath(using: .fillStroke)

let hub = CGRect(x: pt(116 - 12, 174).x, y: pt(116, 174 + 12).y,
                 width: 24 * unit, height: 24 * unit)
ctx.addEllipse(in: hub)
ctx.setFillColor(teal)
ctx.fillPath()

let url = URL(fileURLWithPath: CommandLine.arguments[1]) as CFURL
let dest = CGImageDestinationCreateWithURL(url, UTType.png.identifier as CFString, 1, nil)!
CGImageDestinationAddImage(dest, ctx.makeImage()!, nil)

guard CGImageDestinationFinalize(dest) else {
    FileHandle.standardError.write(Data("icongen: PNG write failed\n".utf8))
    exit(1)
}
