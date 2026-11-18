import XCTest
import UIKit

/// The single long-running "test" that hosts the iadb helper. It never
/// finishes on its own: it serves commands until iadb asks it to stop.
final class IADBRunner: XCTestCase {
  static let helperVersion = "1.0.0"

  // Not called `testRun`: XCTest already has a `testRun` property, and
  // `-only-testing:…/testRun` would silently call that getter instead.
  func testServe() {
    log("helper starting")
    continueAfterFailure = true
    executionTimeAllowance = 60 * 60 * 24 * 365
    let env = ProcessInfo.processInfo.environment
    let port = UInt16(env["IADB_PORT"] ?? "") ?? 47100
    let router = Router()
    let server: HTTPServer
    do {
      server = try HTTPServer(port: port, handler: router.handle)
    } catch {
      XCTFail("iadb: could not listen on port \(port): \(error)")
      return
    }
    server.start()
    log("IADB_READY port=\(port) version=\(IADBRunner.helperVersion)")
    while !router.shouldStop {
      autoreleasepool {
        _ = RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.5))
      }
    }
    log("IADB_STOPPED")
  }
}

/// Writes to stderr, which xcodebuild captures (NSLog goes to the unified log only).
func log(_ message: String) {
  FileHandle.standardError.write(Data("iadb: \(message)\n".utf8))
  // Activity names are always echoed by xcodebuild ("t = 0.01s iadb: …"),
  // unlike stderr from the device, so they double as a reliable log.
  // XCTContext only works on the main thread.
  if Thread.isMainThread {
    XCTContext.runActivity(named: "iadb: \(message)") { _ in }
  }
}

final class Router {
  var shouldStop = false
  private var refs: [Int: CGRect] = [:]
  private var lastLaunched: XCUIApplication?
  private let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")

  func handle(_ request: HTTPRequest) -> HTTPResponse {
    let body = request.json()
    do {
      switch (request.method, request.path) {
      case ("GET", "/status"): return status()
      case ("GET", "/screenshot"): return try screenshot(request.query)
      case ("GET", "/tree"): return try tree(request.query)
      case ("GET", "/app"): return foregroundApp()
      case ("POST", "/tap"): return try tap(body)
      case ("POST", "/swipe"): return swipe(body)
      case ("POST", "/type"): return type(body)
      case ("POST", "/key"): return key(body)
      case ("POST", "/launch"): return launch(body)
      case ("POST", "/terminate"): return terminate(body)
      case ("POST", "/open"): return open(body)
      case ("POST", "/shutdown"):
        log("shutdown requested")
        shouldStop = true
        return .json(["ok": true])
      default:
        return .error("unknown endpoint \(request.method) \(request.path)", status: 404)
      }
    } catch {
      return .error("\(error.localizedDescription)", status: 500)
    }
  }

  // MARK: - Apps

  private func activeApp() -> XCUIApplication {
    if let app = IADBPrivate.activeApplication() {
      IADBPrivate.disableQuiescenceWait(for: app)
      return app
    }
    if let last = lastLaunched, last.state == .runningForeground {
      return last
    }
    return springboard
  }

  private func app(for query: [String: String]) -> XCUIApplication {
    if let bundle = query["app"], !bundle.isEmpty {
      return XCUIApplication(bundleIdentifier: bundle)
    }
    return activeApp()
  }

  private func bundleID(_ app: XCUIApplication) -> String {
    IADBPrivate.bundleID(of: app) ?? (app === springboard ? "com.apple.springboard" : "")
  }

  private func foregroundApp() -> HTTPResponse {
    let app = activeApp()
    return .json(["bundleId": bundleID(app), "name": app.label])
  }

  private func launch(_ body: [String: Any]) -> HTTPResponse {
    guard let bundle = body["bundleId"] as? String, !bundle.isEmpty else { return .error("bundleId is required") }
    let app = XCUIApplication(bundleIdentifier: bundle)
    if body["restart"] as? Bool == true {
      app.terminate()
    }
    app.activate()
    lastLaunched = app
    return .json(["ok": true, "bundleId": bundle, "state": stateName(app.state)])
  }

  private func terminate(_ body: [String: Any]) -> HTTPResponse {
    guard let bundle = body["bundleId"] as? String, !bundle.isEmpty else { return .error("bundleId is required") }
    XCUIApplication(bundleIdentifier: bundle).terminate()
    return .json(["ok": true])
  }

  private func open(_ body: [String: Any]) -> HTTPResponse {
    guard let raw = body["url"] as? String, let url = URL(string: raw) else { return .error("url is required") }
    if #available(iOS 16.4, *) {
      XCUIDevice.shared.system.open(url)
      return .json(["ok": true])
    }
    return .error("opening URLs needs iOS 16.4 or later")
  }

  private func stateName(_ state: XCUIApplication.State) -> String {
    switch state {
    case .notRunning: return "notRunning"
    case .runningBackgroundSuspended: return "suspended"
    case .runningBackground: return "background"
    case .runningForeground: return "foreground"
    default: return "unknown"
    }
  }

  // MARK: - Status & screen

  private func status() -> HTTPResponse {
    let screen = UIScreen.main
    let device = UIDevice.current
    return .json([
      "ok": true,
      "version": IADBRunner.helperVersion,
      "model": device.model,
      "os": "\(device.systemName) \(device.systemVersion)",
      "screen": ["width": screen.bounds.width, "height": screen.bounds.height, "scale": screen.scale],
      "orientation": XCUIDevice.shared.orientation.rawValue,
    ])
  }

  private func screenshot(_ query: [String: String]) throws -> HTTPResponse {
    let image = XCUIScreen.main.screenshot().image
    // scale=points renders one pixel per point so pixel coords == tap coords.
    let pointsMode = query["scale"] == "points"
    let factor = pointsMode ? 1.0 / image.scale : Double(query["scale"] ?? "") ?? 1.0
    var output = image
    if factor < 0.999 || pointsMode {
      let size = CGSize(width: (image.size.width * image.scale * factor).rounded(),
                        height: (image.size.height * image.scale * factor).rounded())
      let format = UIGraphicsImageRendererFormat()
      format.scale = 1
      output = UIGraphicsImageRenderer(size: size, format: format).image { _ in
        image.draw(in: CGRect(origin: .zero, size: size))
      }
    }
    if query["format"] == "jpeg" {
      let quality = Double(query["quality"] ?? "") ?? 0.7
      guard let data = output.jpegData(compressionQuality: quality) else { throw IADBError("could not encode JPEG") }
      return HTTPResponse(status: 200, contentType: "image/jpeg", body: data)
    }
    guard let data = output.pngData() else { throw IADBError("could not encode PNG") }
    return HTTPResponse(status: 200, contentType: "image/png", body: data)
  }

  // MARK: - Accessibility tree

  /// Types worth listing even without any text (you can still tap them).
  /// Unlabeled images, icons and cells are left out: they are usually
  /// decoration or a wrapper around a labeled button with the same frame.
  private static let interactiveTypes: Set<XCUIElement.ElementType> = [
    .button, .textField, .secureTextField, .searchField, .textView, .switch, .toggle, .slider,
    .stepper, .link, .tab, .segmentedControl, .picker, .pickerWheel, .menuItem, .key,
    .checkBox, .radioButton, .datePicker, .pageIndicator, .alert,
  ]

  private func tree(_ query: [String: String]) throws -> HTTPResponse {
    let app = app(for: query)
    let limit = Int(query["limit"] ?? "") ?? 500
    let snapshot = try app.snapshot()
    let screen = UIScreen.main.bounds
    var lines: [String] = []
    var elements: [[String: Any]] = []
    var count = 0
    var truncated = false
    refs = [:]

    func walk(_ node: XCUIElementSnapshot, parentLabel: String, depth: Int) {
      let frame = node.frame
      let label = node.label.trimmingCharacters(in: .whitespacesAndNewlines)
      let value = (node.value as? String)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ((node.value as? NSNumber)?.stringValue ?? "")
      let placeholder = node.placeholderValue ?? ""
      let identifier = node.identifier
      let hasText = !label.isEmpty || !value.isEmpty || !placeholder.isEmpty
      let interesting = (hasText || Router.interactiveTypes.contains(node.elementType))
        && node.elementType != .application && node.elementType != .window
      let visible = !frame.isEmpty && frame.intersects(screen)
      let duplicate = !label.isEmpty && label == parentLabel && node.elementType == .staticText

      var emittedLabel = parentLabel
      if interesting && visible && !duplicate {
        if count >= limit {
          truncated = true
        } else {
          count += 1
          let visibleFrame = frame.intersection(screen)
          refs[count] = visibleFrame
          let cx = Int(visibleFrame.midX.rounded()), cy = Int(visibleFrame.midY.rounded())
          var line = "[\(count)] \(typeName(node.elementType))"
          if !label.isEmpty { line += " \"\(clip(label))\"" }
          if !value.isEmpty && value != label { line += " value=\"\(clip(value))\"" }
          if !placeholder.isEmpty && value.isEmpty { line += " placeholder=\"\(clip(placeholder))\"" }
          if !identifier.isEmpty && identifier != label { line += " id=\(clip(identifier, 40))" }
          if !node.isEnabled { line += " disabled" }
          if node.isSelected { line += " selected" }
          line += " @(\(cx),\(cy)) \(Int(frame.width))x\(Int(frame.height))"
          lines.append(line)
          elements.append([
            "ref": count, "type": typeName(node.elementType), "label": label, "value": value,
            "identifier": identifier, "placeholder": placeholder, "enabled": node.isEnabled,
            "selected": node.isSelected, "x": cx, "y": cy,
            "frame": ["x": frame.minX, "y": frame.minY, "width": frame.width, "height": frame.height],
          ])
          emittedLabel = label
        }
      }
      for child in node.children {
        walk(child, parentLabel: emittedLabel, depth: depth + 1)
      }
    }
    walk(snapshot, parentLabel: "", depth: 0)

    var header = "app: \(bundleID(app)) \"\(app.label)\"  screen: \(Int(screen.width))x\(Int(screen.height)) points"
    if truncated { header += "  (truncated at \(limit) elements)" }
    return .json([
      "bundleId": bundleID(app),
      "text": ([header] + lines).joined(separator: "\n"),
      "elements": elements,
      "truncated": truncated,
    ])
  }

  private func clip(_ s: String, _ max: Int = 80) -> String {
    let flat = s.replacingOccurrences(of: "\n", with: " ").replacingOccurrences(of: "\"", with: "'")
    return flat.count > max ? String(flat.prefix(max - 1)) + "…" : flat
  }

  // MARK: - Input

  private func coordinate(_ x: CGFloat, _ y: CGFloat) -> XCUICoordinate {
    let app = activeApp()
    let origin = app.coordinate(withNormalizedOffset: CGVector(dx: 0, dy: 0))
    let frame = app.frame
    return origin.withOffset(CGVector(dx: x - frame.minX, dy: y - frame.minY))
  }

  private func findElement(text: String) -> XCUIElement? {
    let exact = NSPredicate(format: "label ==[c] %@ OR identifier == %@ OR value ==[c] %@ OR placeholderValue ==[c] %@", text, text, text, text)
    let fuzzy = NSPredicate(format: "label CONTAINS[c] %@ OR value CONTAINS[c] %@", text, text)
    // Search the foreground app first, then SpringBoard (system alerts live there).
    for app in [activeApp(), springboard] {
      for predicate in [exact, fuzzy] {
        let match = app.descendants(matching: .any).matching(predicate).allElementsBoundByIndex
          .first { $0.exists && !$0.frame.isEmpty && $0.frame.intersects(UIScreen.main.bounds) }
        if let match { return match }
      }
    }
    return nil
  }

  private func tap(_ body: [String: Any]) throws -> HTTPResponse {
    var point: CGPoint
    var target = ""
    if let ref = body["ref"] as? Int {
      guard let rect = refs[ref] else { throw IADBError("ref \(ref) not found; run tree again") }
      point = CGPoint(x: rect.midX, y: rect.midY)
      target = "ref \(ref)"
    } else if let text = body["text"] as? String, !text.isEmpty {
      guard let element = findElement(text: text) else { throw IADBError("no element matching \"\(text)\" on screen") }
      let rect = element.frame.intersection(UIScreen.main.bounds)
      point = CGPoint(x: rect.midX, y: rect.midY)
      target = "\"\(element.label.isEmpty ? text : element.label)\""
    } else if let x = number(body["x"]), let y = number(body["y"]) {
      point = CGPoint(x: x, y: y)
      target = "(\(Int(x)),\(Int(y)))"
    } else {
      throw IADBError("tap needs x/y, ref, or text")
    }
    let c = coordinate(point.x, point.y)
    let duration = number(body["duration"]) ?? 0
    let count = body["count"] as? Int ?? 1
    if duration > 0 {
      c.press(forDuration: duration)
    } else if count >= 2 {
      c.doubleTap()
    } else {
      c.tap()
    }
    return .json(["ok": true, "tapped": target, "x": point.x, "y": point.y])
  }

  private func swipe(_ body: [String: Any]) -> HTTPResponse {
    guard let x1 = number(body["x1"]), let y1 = number(body["y1"]),
          let x2 = number(body["x2"]), let y2 = number(body["y2"]) else {
      return .error("swipe needs x1, y1, x2, y2")
    }
    let duration = max(number(body["duration"]) ?? 0.3, 0.05)
    let distance = hypot(x2 - x1, y2 - y1)
    let velocity = XCUIGestureVelocity(rawValue: max(distance / duration, 100))
    coordinate(x1, y1).press(forDuration: 0.05, thenDragTo: coordinate(x2, y2), withVelocity: velocity, thenHoldForDuration: 0)
    return .json(["ok": true])
  }

  private func type(_ body: [String: Any]) -> HTTPResponse {
    guard let text = body["text"] as? String else { return .error("text is required") }
    let app = activeApp()
    let focused = app.descendants(matching: .any).matching(NSPredicate(format: "hasKeyboardFocus == true")).firstMatch
    if focused.exists {
      focused.typeText(text)
    } else {
      app.typeText(text)
    }
    return .json(["ok": true, "focused": focused.exists])
  }

  private func key(_ body: [String: Any]) -> HTTPResponse {
    let name = (body["key"] as? String ?? "").lowercased().replacingOccurrences(of: "_", with: "-")
    switch name {
    case "home":
      XCUIDevice.shared.press(.home)
    case "lock", "power":
      if !IADBPrivate.pressLockButton() { return .error("lock button not supported on this iOS version") }
    case "volume-up", "volumeup":
      #if targetEnvironment(simulator)
      return .error("volume keys are not available in the simulator")
      #else
      XCUIDevice.shared.press(.volumeUp)
      #endif
    case "volume-down", "volumedown":
      #if targetEnvironment(simulator)
      return .error("volume keys are not available in the simulator")
      #else
      XCUIDevice.shared.press(.volumeDown)
      #endif
    case "return", "enter":
      activeApp().typeText("\n")
    case "delete", "backspace":
      activeApp().typeText(XCUIKeyboardKey.delete.rawValue)
    case "tab":
      activeApp().typeText("\t")
    case "escape":
      activeApp().typeText(XCUIKeyboardKey.escape.rawValue)
    default:
      return .error("unknown key \"\(name)\" (home, lock, volume-up, volume-down, return, delete, tab, escape)")
    }
    return .json(["ok": true])
  }

  private func number(_ value: Any?) -> CGFloat? {
    if let n = value as? NSNumber { return CGFloat(n.doubleValue) }
    if let s = value as? String, let d = Double(s) { return CGFloat(d) }
    return nil
  }

  private func typeName(_ type: XCUIElement.ElementType) -> String {
    switch type {
    case .button: return "Button"
    case .staticText: return "Text"
    case .textField: return "TextField"
    case .secureTextField: return "SecureTextField"
    case .searchField: return "SearchField"
    case .textView: return "TextView"
    case .image: return "Image"
    case .icon: return "Icon"
    case .cell: return "Cell"
    case .switch: return "Switch"
    case .toggle: return "Toggle"
    case .slider: return "Slider"
    case .link: return "Link"
    case .tab: return "Tab"
    case .tabBar: return "TabBar"
    case .navigationBar: return "NavBar"
    case .toolbar: return "Toolbar"
    case .alert: return "Alert"
    case .sheet: return "Sheet"
    case .table: return "Table"
    case .collectionView: return "Collection"
    case .scrollView: return "ScrollView"
    case .segmentedControl: return "Segmented"
    case .picker: return "Picker"
    case .pickerWheel: return "PickerWheel"
    case .key: return "Key"
    case .keyboard: return "Keyboard"
    case .menuItem: return "MenuItem"
    case .webView: return "WebView"
    case .stepper: return "Stepper"
    case .pageIndicator: return "PageIndicator"
    case .progressIndicator: return "Progress"
    case .activityIndicator: return "Spinner"
    case .other: return "Element"
    default: return "Element(\(type.rawValue))"
    }
  }
}

struct IADBError: LocalizedError {
  let message: String
  init(_ message: String) { self.message = message }
  var errorDescription: String? { message }
}
