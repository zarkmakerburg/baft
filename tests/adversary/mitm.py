#!/usr/bin/env python3
"""On-path byte-flipper for the integrity intrusion test. Transparent TCP relay
that forwards listen<->upstream and flips one byte once, after FLIP_AFTER bytes
in the client->upstream direction. Models an active on-path attacker tampering
with the encrypted carrier. Loopback only.

  mitm.py LISTEN_PORT UPSTREAM_PORT [FLIP_AFTER]
"""
import socket, sys, threading

def pump(src, dst, flip_after, state, tag):
    sent = 0
    while True:
        try:
            b = src.recv(65536)
        except OSError:
            break
        if not b:
            break
        if flip_after is not None and not state["flipped"] and sent + len(b) > flip_after:
            i = max(0, flip_after - sent)
            ba = bytearray(b)
            if i < len(ba):
                ba[i] ^= 0xFF
                state["flipped"] = True
                b = bytes(ba)
        sent += len(b)
        try:
            dst.sendall(b)
        except OSError:
            break
    try:
        dst.shutdown(socket.SHUT_WR)
    except OSError:
        pass

def handle(client, upstream_port, flip_after):
    try:
        up = socket.create_connection(("127.0.0.1", upstream_port), timeout=10)
    except OSError:
        client.close(); return
    state = {"flipped": False}
    t1 = threading.Thread(target=pump, args=(client, up, flip_after, state, "c>u"))
    t2 = threading.Thread(target=pump, args=(up, client, None, state, "u>c"))
    t1.start(); t2.start(); t1.join(); t2.join()
    client.close(); up.close()

def main():
    listen = int(sys.argv[1]); upstream = int(sys.argv[2])
    flip_after = int(sys.argv[3]) if len(sys.argv) > 3 else None
    srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", listen)); srv.listen(64)
    print(f"mitm {listen}->{upstream} flip_after={flip_after}", flush=True)
    while True:
        c, _ = srv.accept()
        threading.Thread(target=handle, args=(c, upstream, flip_after), daemon=True).start()

if __name__ == "__main__":
    main()
