/* Copyright 2026 Nicolas Cassan. Licensed under Apache-2.0. */
#pragma once

#include <curl/curl.h>

#include <cctype>
#include <mutex>
#include <stdexcept>
#include <string>

namespace wrapper::http {

struct Response { long status = 0; std::string body; };

struct WriteState {
    std::string body;
    std::size_t limit = 0;
    bool exceeded = false;
};

inline std::size_t append_body(char* data, std::size_t size, std::size_t count, void* opaque) {
    auto* state = static_cast<WriteState*>(opaque);
    const std::size_t bytes = size * count;
    if (state->body.size() > state->limit || bytes > state->limit - state->body.size()) {
        state->exceeded = true;
        return 0;
    }
    state->body.append(data, bytes);
    return bytes;
}

inline void ensure_initialized() {
    static std::once_flag once;
    static CURLcode result = CURLE_OK;
    std::call_once(once, [] { result = ::curl_global_init(CURL_GLOBAL_DEFAULT); });
    if (result != CURLE_OK) throw std::runtime_error("libcurl initialization failed");
}

inline Response request(
    const std::string& method,
    const std::string& url,
    const std::string& body = "",
    std::size_t max_body = 128U * 1024U * 1024U
) {
    if (url.rfind("http://", 0) != 0 && url.rfind("https://", 0) != 0) {
        throw std::runtime_error("Matter lease URL must use HTTP or HTTPS");
    }
    ensure_initialized();
    CURL* handle = ::curl_easy_init();
    if (handle == nullptr) throw std::runtime_error("cannot create Matter HTTP client");
    WriteState state{"", max_body, false};
    char error[CURL_ERROR_SIZE]{};
    ::curl_easy_setopt(handle, CURLOPT_URL, url.c_str());
    ::curl_easy_setopt(handle, CURLOPT_CUSTOMREQUEST, method.c_str());
    ::curl_easy_setopt(handle, CURLOPT_FOLLOWLOCATION, 0L);
    ::curl_easy_setopt(handle, CURLOPT_CONNECTTIMEOUT, 30L);
    ::curl_easy_setopt(handle, CURLOPT_TIMEOUT, 300L);
    ::curl_easy_setopt(handle, CURLOPT_NOSIGNAL, 1L);
    ::curl_easy_setopt(handle, CURLOPT_ERRORBUFFER, error);
    ::curl_easy_setopt(handle, CURLOPT_WRITEFUNCTION, append_body);
    ::curl_easy_setopt(handle, CURLOPT_WRITEDATA, &state);
    if (!body.empty() || method == "PUT" || method == "POST") {
        ::curl_easy_setopt(handle, CURLOPT_POSTFIELDS, body.data());
        ::curl_easy_setopt(handle, CURLOPT_POSTFIELDSIZE_LARGE, static_cast<curl_off_t>(body.size()));
    }
    const CURLcode code = ::curl_easy_perform(handle);
    long status = 0;
    ::curl_easy_getinfo(handle, CURLINFO_RESPONSE_CODE, &status);
    ::curl_easy_cleanup(handle);
    if (state.exceeded) throw std::runtime_error("Matter HTTP response exceeds limit");
    if (code != CURLE_OK) {
        throw std::runtime_error(std::string("Matter HTTP request failed: ") +
            (error[0] == '\0' ? ::curl_easy_strerror(code) : error));
    }
    if (status < 200 || status >= 300) throw std::runtime_error("Matter HTTP status " + std::to_string(status));
    return Response{status, std::move(state.body)};
}

inline std::string url_encode(const std::string& value) {
    static const char* hex = "0123456789ABCDEF";
    std::string out;
    for (unsigned char ch : value) {
        if (std::isalnum(ch) || ch == '-' || ch == '_' || ch == '.' || ch == '~') out.push_back(static_cast<char>(ch));
        else { out.push_back('%'); out.push_back(hex[ch >> 4]); out.push_back(hex[ch & 15]); }
    }
    return out;
}

}  // namespace wrapper::http
