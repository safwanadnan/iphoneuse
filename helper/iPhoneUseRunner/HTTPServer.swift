import Foundation
import Network

/// A tiny HTTP/1.1 server (one request per connection) on Network.framework.
/// iphoneuse reaches it over USB through usbmuxd, or on localhost for simulators.
struct HTTPRequest {
  let method: String
  let path: String
  let query: [String: String]
  let body: Data

  func json() -> [String: Any] {
    guard !body.isEmpty,
          let obj = try? JSONSerialization.jsonObject(with: body),
          let dict = obj as? [String: Any] else { return [:] }
    return dict
  }

  /// Returns nil until the full request (headers + Content-Length body) is buffered.
  static func parse(_ data: Data) -> HTTPRequest? {
    let separator = Data("\r\n\r\n".utf8)
    guard let headerEnd = data.range(of: separator) else { return nil }
    guard let head = String(data: data[data.startIndex..<headerEnd.lowerBound], encoding: .utf8) else { return nil }
    var lines = head.components(separatedBy: "\r\n")
    let requestLine = lines.removeFirst().split(separator: " ")
    guard requestLine.count >= 2 else { return nil }
    var contentLength = 0
    for line in lines {
      let parts = line.split(separator: ":", maxSplits: 1)
      if parts.count == 2, parts[0].lowercased() == "content-length" {
        contentLength = Int(parts[1].trimmingCharacters(in: .whitespaces)) ?? 0
      }
    }
    let bodyStart = headerEnd.upperBound
    guard data.distance(from: bodyStart, to: data.endIndex) >= contentLength else { return nil }
    let body = data[bodyStart..<data.index(bodyStart, offsetBy: contentLength)]

    let target = String(requestLine[1])
    let components = URLComponents(string: target)
    var query: [String: String] = [:]
    for item in components?.queryItems ?? [] { query[item.name] = item.value ?? "" }
    return HTTPRequest(method: String(requestLine[0]), path: components?.path ?? target, query: query, body: Data(body))
  }
}

struct HTTPResponse {
  var status: Int = 200
  var contentType = "application/json"
  var body = Data()

  static func json(_ object: Any, status: Int = 200) -> HTTPResponse {
    let data = (try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])) ?? Data("{}".utf8)
    return HTTPResponse(status: status, contentType: "application/json", body: data)
  }

  static func error(_ message: String, status: Int = 400) -> HTTPResponse {
    json(["error": message], status: status)
  }

  func serialize() -> Data {
    let reason = status == 200 ? "OK" : (status == 404 ? "Not Found" : "Error")
    var out = Data("HTTP/1.1 \(status) \(reason)\r\nContent-Type: \(contentType)\r\nContent-Length: \(body.count)\r\nConnection: close\r\n\r\n".utf8)
    out.append(body)
    return out
  }
}

final class HTTPServer {
  private let listener: NWListener
  private let queue = DispatchQueue(label: "dev.iphoneuse.http")
  private let handler: (HTTPRequest) -> HTTPResponse

  init(port: UInt16, handler: @escaping (HTTPRequest) -> HTTPResponse) throws {
    let params = NWParameters.tcp
    params.allowLocalEndpointReuse = true
    guard let nwPort = NWEndpoint.Port(rawValue: port) else { throw NSError(domain: "iphoneuse", code: 1) }
    listener = try NWListener(using: params, on: nwPort)
    self.handler = handler
  }

  func start() {
    listener.stateUpdateHandler = { state in log("listener \(state)") }
    listener.newConnectionHandler = { [weak self] connection in
      guard let self else { return }
      connection.start(queue: self.queue)
      self.receive(connection, buffer: Data())
    }
    listener.start(queue: queue)
  }

  private func receive(_ connection: NWConnection, buffer: Data) {
    connection.receive(minimumIncompleteLength: 1, maximumLength: 1 << 20) { [weak self] data, _, isComplete, error in
      guard let self else { return }
      var buffer = buffer
      if let data { buffer.append(data) }
      if let request = HTTPRequest.parse(buffer) {
        // XCTest must be driven from the main thread; the test method keeps the
        // main run loop spinning so these blocks run one at a time.
        DispatchQueue.main.async {
          let response = self.handler(request)
          connection.send(content: response.serialize(), completion: .contentProcessed { _ in connection.cancel() })
        }
        return
      }
      if isComplete || error != nil {
        connection.cancel()
        return
      }
      self.receive(connection, buffer: buffer)
    }
  }
}
