#!/usr/bin/env python3
import argparse, socketserver

class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        while True:
            data=self.request.recv(65536)
            if not data:
                return
            self.request.sendall(data)

class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address=True
    daemon_threads=True

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("--host",default="127.0.0.1")
    ap.add_argument("--port",type=int,default=19090)
    a=ap.parse_args()
    with Server((a.host,a.port),Handler) as s:
        s.serve_forever()

if __name__=="__main__": main()
