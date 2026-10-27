// SPDX-License-Identifier: Apache-2.0
//
// Regenerates the fixtures with Apple's ImageIO, the encoder an iPhone uses
// (macOS only):
//
//	cd internal/media/testdata && swift make.swift
//
// Each image is two halves, so a test can tell the orientation: the first
// colour is the left half as stored, before the orientation applies.
import CoreGraphics
import Foundation
import ImageIO

func halves(_ w: Int, _ h: Int, _ space: CFString, _ a: [CGFloat], _ b: [CGFloat]) -> CGImage {
    let cs = CGColorSpace(name: space)!
    let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0, space: cs,
                        bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
    ctx.setFillColor(CGColor(colorSpace: cs, components: a + [1])!)
    ctx.fill(CGRect(x: 0, y: 0, width: w / 2, height: h))
    ctx.setFillColor(CGColor(colorSpace: cs, components: b + [1])!)
    ctx.fill(CGRect(x: w / 2, y: 0, width: w - w / 2, height: h))
    return ctx.makeImage()!
}

func write(_ name: String, _ type: String, _ img: CGImage, orientation: Int) {
    let dst = CGImageDestinationCreateWithURL(URL(fileURLWithPath: name) as CFURL, type as CFString, 1, nil)!
    let props: [CFString: Any] = [
        kCGImagePropertyOrientation: orientation,
        kCGImageDestinationLossyCompressionQuality: 0.85,
        kCGImagePropertyGPSDictionary: [
            kCGImagePropertyGPSLatitude: 50.4501, kCGImagePropertyGPSLatitudeRef: "N",
            kCGImagePropertyGPSLongitude: 30.5234, kCGImagePropertyGPSLongitudeRef: "E",
        ],
        kCGImagePropertyExifDictionary: [kCGImagePropertyExifLensModel: "iPhone 17 Pro back camera 6.86mm f/1.78"],
    ]
    CGImageDestinationAddImage(dst, img, props as CFDictionary)
    guard CGImageDestinationFinalize(dst) else { fatalError(name) }
}

let p3 = CGColorSpace.displayP3, srgb = CGColorSpace.sRGB
// An iPhone photo: HEIC in 512 px tiles, Display P3, turned a quarter by
// orientation 6, with GPS.
write("iphone.heic", "public.heic", halves(1600, 1200, p3, [0.60, 0.45, 0.30], [0.25, 0.40, 0.65]), orientation: 6)
// A tiled AVIF. ImageIO writes its orientation 3 into EXIF only and leaves
// irot at 0, so readers, ffmpeg and macOS alike, show it as stored.
write("tiled.avif", "public.avif", halves(1600, 1200, srgb, [0.80, 0.20, 0.20], [0.20, 0.30, 0.80]), orientation: 3)
// A JPEG turned the other quarter by EXIF, with GPS.
write("exif.jpg", "public.jpeg", halves(400, 300, srgb, [0.80, 0.20, 0.20], [0.20, 0.30, 0.80]), orientation: 8)
// A Display P3 JPEG, as the iPhone's "Most Compatible" setting saves.
write("p3.jpg", "public.jpeg", halves(400, 300, p3, [0.60, 0.45, 0.30], [0.25, 0.40, 0.65]), orientation: 1)
// small.avif, a plain AVIF without a grid, comes from ffmpeg's own encoder:
//
//	ffmpeg -f lavfi -i "color=0xcc3333:s=160x240[a];color=0x334dcc:s=160x240[b];[a][b]hstack" \
//	  -frames:v 1 -c:v libsvtav1 small.avif
