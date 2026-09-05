/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

#pragma once

#include <arpa/inet.h>
#include <netdb.h>
#include <sys/socket.h>
#include <unistd.h>

#include <array>
#include <cerrno>
#include <cstdint>
#include <cstring>
#include <random>
#include <stdexcept>
#include <string>
#include <vector>

namespace wrapper {

struct WebSocketURL {
    std::string host;
    std::string port;
    std::string path;
};

inline std::string base64_encode(const std::string& input) {
    static constexpr char kAlphabet[] =
        "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    std::string out;
    out.reserve(((input.size() + 2) / 3) * 4);
    std::size_t i = 0;
    while (i + 2 < input.size()) {
        const std::uint32_t n = (static_cast<unsigned char>(input[i]) << 16U) |
                                (static_cast<unsigned char>(input[i + 1]) << 8U) |
                                static_cast<unsigned char>(input[i + 2]);
        out.push_back(kAlphabet[(n >> 18U) & 0x3F]);
        out.push_back(kAlphabet[(n >> 12U) & 0x3F]);
        out.push_back(kAlphabet[(n >> 6U) & 0x3F]);
        out.push_back(kAlphabet[n & 0x3F]);
        i += 3;
    }
    if (i < input.size()) {
        std::uint32_t n = static_cast<unsigned char>(input[i]) << 16U;
        out.push_back(kAlphabet[(n >> 18U) & 0x3F]);
        if (i + 1 < input.size()) {
            n |= static_cast<unsigned char>(input[i + 1]) << 8U;
            out.push_back(kAlphabet[(n >> 12U) & 0x3F]);
            out.push_back(kAlphabet[(n >> 6U) & 0x3F]);
            out.push_back('=');
        } else {
            out.push_back(kAlphabet[(n >> 12U) & 0x3F]);
            out.push_back('=');
            out.push_back('=');
        }
    }
    return out;
}

inline WebSocketURL parse_ws_url(const std::string& raw) {
    const std::string prefix = "ws://";
    if (raw.rfind(prefix, 0) != 0) {
        throw std::runtime_error("unsupported websocket url: " + raw);
    }
    const std::string rest = raw.substr(prefix.size());
    const std::size_t slash = rest.find('/');
    const std::string hostport = slash == std::string::npos ? rest : rest.substr(0, slash);
    const std::string path = slash == std::string::npos ? "/" : rest.substr(slash);
    const std::size_t colon = hostport.rfind(':');
    if (colon == std::string::npos) {
        throw std::runtime_error("websocket url missing port: " + raw);
    }
    WebSocketURL url;
    url.host = hostport.substr(0, colon);
    url.port = hostport.substr(colon + 1);
    url.path = path.empty() ? "/" : path;
    return url;
}

class WSClient {
public:
    WSClient() = default;
    ~WSClient() { close(); }

    void connect(const std::string& url_raw) {
        const WebSocketURL url = parse_ws_url(url_raw);
        socket_fd_ = connect_tcp(url.host, url.port);
        const std::string key = random_key();
        const std::string request =
            "GET " + url.path + " HTTP/1.1\r\n"
            "Host: " + url.host + ":" + url.port + "\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            "Sec-WebSocket-Key: " + key + "\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            "\r\n";
        write_all(request.data(), request.size());
        const std::string response = read_http_response();
        if (response.find(" 101 ") == std::string::npos) {
            throw std::runtime_error("websocket handshake failed: " + response);
        }
    }

    void close() {
        if (socket_fd_ >= 0) {
            ::close(socket_fd_);
            socket_fd_ = -1;
        }
    }

    void send_text(const std::string& payload) {
        send_frame(0x1, payload);
    }

    void send_pong(const std::string& payload) {
        send_frame(0xA, payload);
    }

    bool receive_text(std::string& out) {
        while (true) {
            int opcode = 0;
            std::string payload;
            if (!read_frame(opcode, payload)) {
                return false;
            }
            if (opcode == 0x1) {
                out = payload;
                return true;
            }
            if (opcode == 0x8) {
                return false;
            }
            if (opcode == 0x9) {
                send_pong(payload);
                continue;
            }
        }
    }

private:
    int socket_fd_ = -1;

    static std::string random_key() {
        std::array<unsigned char, 16> bytes{};
        std::random_device rd;
        for (auto& b : bytes) {
            b = static_cast<unsigned char>(rd());
        }
        return base64_encode(std::string(reinterpret_cast<const char*>(bytes.data()), bytes.size()));
    }

    static int connect_tcp(const std::string& host, const std::string& port) {
        addrinfo hints{};
        hints.ai_family = AF_UNSPEC;
        hints.ai_socktype = SOCK_STREAM;
        addrinfo* result = nullptr;
        const int rc = ::getaddrinfo(host.c_str(), port.c_str(), &hints, &result);
        if (rc != 0) {
            throw std::runtime_error("getaddrinfo failed for " + host + ":" + port + ": " + gai_strerror(rc));
        }
        int fd = -1;
        for (addrinfo* rp = result; rp != nullptr; rp = rp->ai_next) {
            fd = ::socket(rp->ai_family, rp->ai_socktype, rp->ai_protocol);
            if (fd < 0) {
                continue;
            }
            if (::connect(fd, rp->ai_addr, rp->ai_addrlen) == 0) {
                break;
            }
            ::close(fd);
            fd = -1;
        }
        ::freeaddrinfo(result);
        if (fd < 0) {
            throw std::runtime_error("tcp connect failed for " + host + ":" + port);
        }
        return fd;
    }

    std::string read_http_response() {
        std::string out;
        std::array<char, 1024> buf{};
        while (out.find("\r\n\r\n") == std::string::npos) {
            const ssize_t n = ::recv(socket_fd_, buf.data(), buf.size(), 0);
            if (n <= 0) {
                throw std::runtime_error("websocket handshake response read failed");
            }
            out.append(buf.data(), static_cast<std::size_t>(n));
        }
        return out;
    }

    void write_all(const void* data, std::size_t len) {
        const char* ptr = static_cast<const char*>(data);
        std::size_t sent = 0;
        while (sent < len) {
            const ssize_t n = ::send(socket_fd_, ptr + sent, len - sent, 0);
            if (n <= 0) {
                throw std::runtime_error("socket write failed");
            }
            sent += static_cast<std::size_t>(n);
        }
    }

    void send_frame(int opcode, const std::string& payload) {
        std::vector<std::uint8_t> frame;
        frame.push_back(static_cast<std::uint8_t>(0x80 | (opcode & 0x0F)));
        const std::size_t size = payload.size();
        if (size < 126) {
            frame.push_back(static_cast<std::uint8_t>(0x80 | size));
        } else if (size <= 0xFFFF) {
            frame.push_back(0x80 | 126);
            frame.push_back(static_cast<std::uint8_t>((size >> 8U) & 0xFF));
            frame.push_back(static_cast<std::uint8_t>(size & 0xFF));
        } else {
            frame.push_back(0x80 | 127);
            for (int i = 7; i >= 0; --i) {
                frame.push_back(static_cast<std::uint8_t>((static_cast<std::uint64_t>(size) >> (i * 8U)) & 0xFF));
            }
        }
        std::array<std::uint8_t, 4> mask{};
        std::random_device rd;
        for (auto& byte : mask) {
            byte = static_cast<std::uint8_t>(rd());
            frame.push_back(byte);
        }
        const std::size_t offset = frame.size();
        frame.resize(offset + size);
        for (std::size_t i = 0; i < size; ++i) {
            frame[offset + i] = static_cast<std::uint8_t>(payload[i]) ^ mask[i % 4];
        }
        write_all(frame.data(), frame.size());
    }

    bool read_frame(int& opcode, std::string& payload) {
        std::uint8_t header[2];
        if (!read_exact(header, sizeof(header))) {
            return false;
        }
        opcode = header[0] & 0x0F;
        const bool masked = (header[1] & 0x80) != 0;
        std::uint64_t len = header[1] & 0x7F;
        if (len == 126) {
            std::uint8_t ext[2];
            if (!read_exact(ext, sizeof(ext))) {
                return false;
            }
            len = (static_cast<std::uint64_t>(ext[0]) << 8U) | static_cast<std::uint64_t>(ext[1]);
        } else if (len == 127) {
            std::uint8_t ext[8];
            if (!read_exact(ext, sizeof(ext))) {
                return false;
            }
            len = 0;
            for (unsigned char byte : ext) {
                len = (len << 8U) | static_cast<std::uint64_t>(byte);
            }
        }
        std::array<std::uint8_t, 4> mask{};
        if (masked) {
            if (!read_exact(mask.data(), mask.size())) {
                return false;
            }
        }
        payload.assign(static_cast<std::size_t>(len), '\0');
        if (len > 0 && !read_exact(payload.data(), static_cast<std::size_t>(len))) {
            return false;
        }
        if (masked) {
            for (std::size_t i = 0; i < payload.size(); ++i) {
                payload[i] = static_cast<char>(static_cast<unsigned char>(payload[i]) ^ mask[i % 4]);
            }
        }
        return true;
    }

    bool read_exact(void* out, std::size_t len) {
        char* ptr = static_cast<char*>(out);
        std::size_t got = 0;
        while (got < len) {
            const ssize_t n = ::recv(socket_fd_, ptr + got, len - got, 0);
            if (n == 0) {
                return false;
            }
            if (n < 0) {
                if (errno == EINTR) {
                    continue;
                }
                throw std::runtime_error("socket read failed");
            }
            got += static_cast<std::size_t>(n);
        }
        return true;
    }
};

}  // namespace wrapper
