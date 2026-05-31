// vision_ocr <image_path>  ->  prints recognized text (lines joined) to stdout.
//
// The bundled macOS OCR engine for seek's v7 柱 Q image→text input
// expansion. Built at install/release time with `swiftc -O` (see
// scripts/build-vision-ocr.sh) and placed next to the seek binary; seek
// finds it via os.Executable()+"/vision_ocr". Only depends on system
// frameworks (Vision/AppKit) — no third-party deps, ~70KB output.
//
// seek passes the recognition languages via SEEK_OCR_LANGUAGES (comma-
// separated); defaults to Simplified-Chinese + English for mixed text.
import Foundation
import Vision
import AppKit

guard CommandLine.arguments.count >= 2 else {
    FileHandle.standardError.write("usage: vision_ocr <image>\n".data(using: .utf8)!)
    exit(2)
}
let path = CommandLine.arguments[1]
guard let img = NSImage(contentsOfFile: path),
      let cg = img.cgImage(forProposedRect: nil, context: nil, hints: nil) else {
    FileHandle.standardError.write("cannot load image\n".data(using: .utf8)!)
    exit(1)
}

let req = VNRecognizeTextRequest()
req.recognitionLevel = .accurate
req.usesLanguageCorrection = true
if let env = ProcessInfo.processInfo.environment["SEEK_OCR_LANGUAGES"], !env.isEmpty {
    req.recognitionLanguages = env.split(separator: ",").map {
        $0.trimmingCharacters(in: .whitespaces)
    }
} else {
    req.recognitionLanguages = ["zh-Hans", "en-US"]   // 中英混排
}

do {
    try VNImageRequestHandler(cgImage: cg, options: [:]).perform([req])
} catch {
    FileHandle.standardError.write("perform failed: \(error)\n".data(using: .utf8)!)
    exit(1)
}

var lines: [String] = []
for obs in (req.results ?? []) {
    if let t = obs.topCandidates(1).first {
        lines.append(t.string)
    }
}
print(lines.joined(separator: "\n"))
