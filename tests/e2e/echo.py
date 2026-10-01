#!/usr/bin/env python3
"""Tiny TCP helper for pair_and_run.sh.

  echo.py serve PORT              echo every connection back until EOF
  echo.py send PORT MIB CONNS     send random data over CONNS parallel
                                  connections and require identical bytes back
"""
import hashlib, os, socket, socketserver, sys, threading


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        while True:
            b = self.request.recv(65536)
            if not b:
                break
            self.request.sendall(b)
        self.request.shutdown(socket.SHUT_WR)


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def one(port, size, errors, idx):
    try:
        data = os.urandom(size)
        s = socket.create_connection(("127.0.0.1", port), timeout=60)
        s.settimeout(60)
        got = bytearray()

        def reader():
            while True:
                b = s.recv(65536)
                if not b:
                    return
                got.extend(b)

        t = threading.Thread(target=reader)
        t.start()
        s.sendall(data)
        s.shutdown(socket.SHUT_WR)
        t.join(120)
        s.close()
        want, have = hashlib.sha256(data).hexdigest(), hashlib.sha256(bytes(got)).hexdigest()
        if len(got) != size or want != have:
            errors.append(f"conn {idx}: sent {size} bytes sha256={want}, got {len(got)} sha256={have}")
        else:
            print(f"conn {idx}: {size} bytes round-tripped, sha256={want}")
    except Exception as e:  # noqa: BLE001 - report every failure
        errors.append(f"conn {idx}: {e!r}")


def main():
    if sys.argv[1] == "serve":
        Server(("127.0.0.1", int(sys.argv[2])), Echo).serve_forever()
    elif sys.argv[1] == "send":
        port, mib, conns = int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
        errors, threads = [], []
        for i in range(conns):
            t = threading.Thread(target=one, args=(port, mib << 20, errors, i))
            t.start()
            threads.append(t)
        for t in threads:
            t.join()
        if errors:
            print("\n".join(errors), file=sys.stderr)
            sys.exit(1)
    else:
        sys.exit("usage: echo.py serve PORT | send PORT MIB CONNS")


if __name__ == "__main__":
    main()
