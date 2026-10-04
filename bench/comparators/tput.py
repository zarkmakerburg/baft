#!/usr/bin/env python3
"""Echo throughput probe shared by every tunnel under test.

  tput.py serve PORT            echo every connection back until EOF
  tput.py run PORT MIB CONNS    send MIB per connection over CONNS parallel
                                connections, read the echo back concurrently,
                                verify SHA-256, print one JSON line

The same probe and the same echo target are used for BAFT, FRP, Rathole,
Xray and direct TCP, so only the tunnel differs.
"""
import hashlib, json, os, socket, socketserver, sys, threading, time

CHUNK = 64 * 1024


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        while True:
            b = self.request.recv(CHUNK)
            if not b:
                break
            self.request.sendall(b)
        try:
            self.request.shutdown(socket.SHUT_WR)
        except OSError:
            pass


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def one(port, size, out, idx):
    data = os.urandom(size)
    s = socket.create_connection(("127.0.0.1", port), timeout=30)
    s.settimeout(600)
    h = hashlib.sha256()
    got = [0]

    def reader():
        while True:
            b = s.recv(CHUNK)
            if not b:
                return
            h.update(b)
            got[0] += len(b)

    t = threading.Thread(target=reader)
    start = time.monotonic()
    t.start()
    view = memoryview(data)
    for off in range(0, size, CHUNK):
        s.sendall(view[off:off + CHUNK])
    s.shutdown(socket.SHUT_WR)
    t.join(900)
    elapsed = time.monotonic() - start
    s.close()
    ok = got[0] == size and h.hexdigest() == hashlib.sha256(data).hexdigest()
    out[idx] = (ok, elapsed, got[0])


def main():
    if sys.argv[1] == "serve":
        Server(("127.0.0.1", int(sys.argv[2])), Echo).serve_forever()
        return
    port, mib, conns = int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
    size = mib << 20
    out = [None] * conns
    threads = [threading.Thread(target=one, args=(port, size, out, i)) for i in range(conns)]
    start = time.monotonic()
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    wall = time.monotonic() - start
    ok = all(o is not None and o[0] for o in out)
    # Per direction: every byte crosses the tunnel once each way.
    mbps = (size * conns * 8) / wall / 1e6 if ok else 0.0
    print(json.dumps({"ok": ok, "mib_per_conn": mib, "conns": conns, "seconds": round(wall, 4),
                      "mbps_per_direction": round(mbps, 3)}))
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
