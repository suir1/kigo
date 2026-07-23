#!/usr/bin/env python3

import argparse
import select
import socket
import socketserver


MAX_HEADER = 64 * 1024


class ConnectHandler(socketserver.BaseRequestHandler):
    def handle(self):
        request = bytearray()
        while b"\r\n\r\n" not in request:
            chunk = self.request.recv(4096)
            if not chunk:
                return
            request.extend(chunk)
            if len(request) > MAX_HEADER:
                self.request.sendall(b"HTTP/1.1 431 Request Header Fields Too Large\r\n\r\n")
                return

        first_line = bytes(request).split(b"\r\n", 1)[0]
        fields = first_line.decode("ascii", "replace").split()
        if len(fields) != 3 or fields[0] != "CONNECT":
            self.request.sendall(b"HTTP/1.1 405 Method Not Allowed\r\n\r\n")
            return

        host, port = split_target(fields[1])
        try:
            upstream = socket.create_connection((host, port), timeout=5)
        except OSError:
            self.request.sendall(b"HTTP/1.1 502 Bad Gateway\r\n\r\n")
            return

        print(f"CONNECT {fields[1]}", flush=True)
        with upstream:
            self.request.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            tunnel(self.request, upstream)


def split_target(target):
    if target.startswith("["):
        host, separator, port = target[1:].partition("]:")
    else:
        host, separator, port = target.rpartition(":")
    if not separator or not host:
        raise ValueError(f"invalid CONNECT target: {target}")
    return host, int(port)


def tunnel(client, upstream):
    sockets = [client, upstream]
    while sockets:
        readable, _, _ = select.select(sockets, [], [], 10)
        if not readable:
            continue
        for source in readable:
            data = source.recv(64 * 1024)
            if not data:
                return
            destination = upstream if source is client else client
            destination.sendall(data)


class ThreadingTCPServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = True
    daemon_threads = True


def main():
    parser = argparse.ArgumentParser(description="Minimal HTTP CONNECT proxy for kigo smoke tests")
    parser.add_argument("--listen", default="127.0.0.1:19094")
    args = parser.parse_args()
    host, port = split_target(args.listen)
    with ThreadingTCPServer((host, port), ConnectHandler) as server:
        server.serve_forever()


if __name__ == "__main__":
    main()
