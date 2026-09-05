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

#include <ctime>
#include <filesystem>
#include <functional>
#include <memory>
#include <stdexcept>
#include <string>
#include <unordered_map>
#include <utility>
#include <vector>

#include "constants.hpp"
#include "json.hpp"
#include "ws_client.hpp"

namespace wrapper {

inline std::string env_or(const char* key, const std::string& fallback = "") {
    const char* value = std::getenv(key);
    if (value == nullptr) {
        return fallback;
    }
    return std::string(value);
}

inline std::string now_rfc3339() {
    std::time_t now = std::time(nullptr);
    std::tm tm{};
#if defined(_WIN32)
    gmtime_s(&tm, &now);
#else
    gmtime_r(&now, &tm);
#endif
    char buffer[32];
    std::strftime(buffer, sizeof(buffer), "%Y-%m-%dT%H:%M:%SZ", &tm);
    return std::string(buffer);
}

inline std::string new_id() {
    static constexpr char kHex[] = "0123456789abcdef";
    std::string out(32, '0');
    std::random_device rd;
    for (char& ch : out) {
        ch = kHex[rd() & 0xF];
    }
    return out;
}

class ParamsView {
public:
    explicit ParamsView(std::string raw) : raw_(std::move(raw)) {}

    std::string string_value(const std::string& key, const std::string& fallback = "") const {
        return json::string_field(raw_, key, fallback);
    }

    bool has(const std::string& key) const {
        return json::has_key(raw_, key);
    }

    const std::string& raw() const {
        return raw_;
    }

private:
    std::string raw_;
};

struct CapacityBinding {
    std::string relative_context;
    std::string name;
    std::function<std::string(const ParamsView&)> callback;
};

struct BindingsFragment {
    std::vector<CapacityBinding> capacities;
};

class Runtime {
public:
    Runtime()
        : ctx_dir_(std::filesystem::absolute(env_or("BRIQUE_CTX_DIR"))),
          ctx_id_(env_or("BRIQUE_CTX_ID")),
          wrapper_name_(env_or("BRIQUE_WRAPPER_NAME")),
          wrapper_src_dir_(std::filesystem::absolute(
              env_or("BRIQUE_WRAPPER_SRC_DIR", (ctx_dir_ / "code" / wrapper_name_).string()))),
          wrapper_build_dir_(std::filesystem::absolute(
              env_or("BRIQUE_WRAPPER_BUILD_DIR", (ctx_dir_ / "build" / wrapper_name_).string()))),
          wrapper_work_dir_(std::filesystem::absolute(
              env_or("BRIQUE_WORKDIR", (ctx_dir_ / "tmp" / ("wrapper_" + wrapper_name_)).string()))) {}

    void load(const BindingsFragment& fragment) {
        if (ctx_dir_.empty() || !std::filesystem::exists(ctx_dir_)) {
            throw std::runtime_error("missing BRIQUE_CTX_DIR");
        }
        if (wrapper_name_.empty()) {
            throw std::runtime_error("missing BRIQUE_WRAPPER_NAME");
        }
        std::filesystem::create_directories(wrapper_work_dir_);
        for (const auto& binding : fragment.capacities) {
            capacity_bindings_[make_key(binding.relative_context, binding.name)] = binding.callback;
        }
        transport_.connect(boundary_url());
    }

    int run() {
        transport_.send_text(wrapper_ready_message());
        std::string raw;
        while (!stop_requested_ && transport_.receive_text(raw)) {
            route(raw);
        }
        transport_.close();
        return 0;
    }

    const std::string& ctx_id() const { return ctx_id_; }
    const std::string& wrapper_name() const { return wrapper_name_; }

private:
    using CapacityFn = std::function<std::string(const ParamsView&)>;

    std::filesystem::path ctx_dir_;
    std::string ctx_id_;
    std::string wrapper_name_;
    std::filesystem::path wrapper_src_dir_;
    std::filesystem::path wrapper_build_dir_;
    std::filesystem::path wrapper_work_dir_;
    WSClient transport_;
    bool stop_requested_ = false;
    std::unordered_map<std::string, CapacityFn> capacity_bindings_;

    static std::string make_key(const std::string& relative_context, const std::string& name) {
        std::string rel = relative_context;
        while (!rel.empty() && rel.front() == '/') {
            rel.erase(rel.begin());
        }
        while (!rel.empty() && rel.back() == '/') {
            rel.pop_back();
        }
        return rel + "\n" + name;
    }

    std::string boundary_url() const {
        const std::string raw = read_file((ctx_dir_ / "context.json").string());
        const std::string interfaces = json::array_field(raw, "interfaces", "[]");
        for (const auto& item : split_top_level_objects(interfaces)) {
            if (json::string_field(item, "type") != "wrapper") {
                continue;
            }
            if (json::string_field(item, "name") != wrapper_name_) {
                continue;
            }
            if (json::string_field(item, "driver") != "ws") {
                continue;
            }
            const std::string cfg = json::object_field(item, "config", "{}");
            std::string path = json::string_field(cfg, "path", "/ws");
            std::string base_url = env_or("BRIQUE_WS_URL");
            while (!base_url.empty() && base_url.back() == '/') {
                base_url.pop_back();
            }
            if (!base_url.empty()) {
                return base_url + path;
            }
            std::string addr = json::string_field(cfg, "addr", env_or("BRIQUE_WS_ADDR"));
            if (addr.empty()) {
                throw std::runtime_error("shared websocket listener addr not provided");
            }
            std::string host = "127.0.0.1";
            std::string port;
            if (!addr.empty() && addr.front() == ':') {
                port = addr.substr(1);
            } else {
                const std::size_t colon = addr.rfind(':');
                if (colon != std::string::npos) {
                    host = addr.substr(0, colon);
                    port = addr.substr(colon + 1);
                    if (host.empty() || host == "0.0.0.0") {
                        host = "127.0.0.1";
                    }
                }
            }
            if (port.empty()) {
                throw std::runtime_error("shared websocket listener addr missing port");
            }
            return "ws://" + host + ":" + port + path;
        }
        throw std::runtime_error("wrapper websocket interface not found for " + wrapper_name_);
    }

    static std::string read_file(const std::string& path) {
        FILE* file = std::fopen(path.c_str(), "rb");
        if (file == nullptr) {
            throw std::runtime_error("failed to read " + path);
        }
        std::string out;
        char buffer[4096];
        while (true) {
            const std::size_t n = std::fread(buffer, 1, sizeof(buffer), file);
            if (n > 0) {
                out.append(buffer, n);
            }
            if (n < sizeof(buffer)) {
                if (std::feof(file) != 0) {
                    break;
                }
                std::fclose(file);
                throw std::runtime_error("failed reading " + path);
            }
        }
        std::fclose(file);
        return out;
    }

    static std::vector<std::string> split_top_level_objects(const std::string& array_raw) {
        std::vector<std::string> out;
        std::size_t i = json::skip_ws(array_raw, 0);
        if (i >= array_raw.size() || array_raw[i] != '[') {
            return out;
        }
        ++i;
        while (i < array_raw.size()) {
            i = json::skip_ws(array_raw, i);
            if (i >= array_raw.size() || array_raw[i] == ']') {
                break;
            }
            if (array_raw[i] != '{') {
                ++i;
                continue;
            }
            const std::size_t end = json::match_balanced(array_raw, i, '{', '}');
            out.push_back(array_raw.substr(i, end - i + 1));
            i = end + 1;
        }
        return out;
    }

    void route(const std::string& raw) {
        if (json::string_field(raw, "kind") != kKindIntention) {
            return;
        }
        const std::string intention = json::object_field(raw, "intention", "{}");
        const std::string to = json::object_field(intention, "to", "{}");
        const std::string from = json::object_field(intention, "from", "{}");
        const std::string params_raw = json::object_field(intention, "params", "{}");
        const std::string intention_id = json::string_field(intention, "intention_id");
        const std::string to_type = json::string_field(to, "type");
        const std::string to_cap = json::string_field(to, "cap");

        if (to_type == kTypeExecution && to_cap == kCapWrapperStop) {
            stop_requested_ = true;
            return;
        }
        if (to_type != kTypeUser) {
            return;
        }

        const std::string rel_ctx = target_context(json::string_field(to, "context"));
        const auto it = capacity_bindings_.find(make_key(rel_ctx, to_cap));
        if (it == capacity_bindings_.end()) {
            transport_.send_text(error_response(intention_id, from, to_cap, to_type, json::string_field(to, "context"),
                                                "not_found", "unknown wrapper capacity",
                                                "{\"reason\":\"unknown_cap\"}"));
            return;
        }

        try {
            const std::string payload = it->second(ParamsView(params_raw));
            transport_.send_text(ok_response(intention_id, from, to_cap, to_type, json::string_field(to, "context"), payload));
        } catch (const std::exception& ex) {
            transport_.send_text(error_response(intention_id, from, to_cap, to_type, json::string_field(to, "context"),
                                                "internal", ex.what(),
                                                "{\"reason\":\"wrapper_capacity_failed\"}"));
        }
    }

    std::string target_context(const std::string& raw_context) const {
        const std::string prefix = "@wrapper_" + wrapper_name_ + ":/";
        if (raw_context.rfind(prefix, 0) == 0) {
            return trim_slashes(raw_context.substr(prefix.size()));
        }
        return trim_slashes(raw_context);
    }

    std::string response_from_context(const std::string& raw_context) const {
        const std::string prefix = "@wrapper_" + wrapper_name_ + ":/";
        if (raw_context.rfind(prefix, 0) == 0) {
            return trim_slashes(raw_context.substr(prefix.size()));
        }
        return "";
    }

    static std::string trim_slashes(std::string value) {
        while (!value.empty() && value.front() == '/') {
            value.erase(value.begin());
        }
        while (!value.empty() && value.back() == '/') {
            value.pop_back();
        }
        return value;
    }

    std::string wrapper_ready_message() const {
        return "{"
               "\"kind\":\"intention\","
               "\"ts\":" + json::quote(now_rfc3339()) + ","
               "\"intention\":{"
               "\"intention_id\":" + json::quote(new_id()) + ","
               "\"await_response\":false,"
               "\"to\":{"
               "\"context\":" + json::quote(ctx_id_) + ","
               "\"cap\":\"wrapper_ready\","
               "\"type\":\"execution\""
               "},"
               "\"from\":{"
               "\"context\":\"\","
               "\"cap\":\"\","
               "\"type\":\"execution\""
               "},"
               "\"identity\":{"
               "\"id\":" + json::quote(wrapper_name_) + ","
               "\"kind\":\"wrapper\""
               "},"
               "\"params\":{"
               "\"wrapper\":" + json::quote(wrapper_name_) +
               "},"
               "\"correlation\":{"
               "\"root_intention_id\":\"\","
               "\"parent_intention_id\":\"\""
               "}"
               "}"
               "}";
    }

    std::string ok_response(
        const std::string& intention_id,
        const std::string& to_raw,
        const std::string& cap,
        const std::string& type,
        const std::string& to_context_raw,
        const std::string& payload_raw
    ) const {
        return "{"
               "\"kind\":\"response\","
               "\"ts\":" + json::quote(now_rfc3339()) + ","
               "\"response\":{"
               "\"intention_id\":" + json::quote(intention_id) + ","
               "\"to\":" + to_raw + ","
               "\"from\":{"
               "\"context\":" + json::quote(response_from_context(to_context_raw)) + ","
               "\"cap\":" + json::quote(cap) + ","
               "\"type\":" + json::quote(type) +
               "},"
               "\"identity\":{"
               "\"id\":" + json::quote(wrapper_name_) + ","
               "\"kind\":\"wrapper\""
               "},"
               "\"status\":\"ok\","
               "\"payload\":" + payload_raw +
               "}"
               "}";
    }

    std::string error_response(
        const std::string& intention_id,
        const std::string& to_raw,
        const std::string& cap,
        const std::string& type,
        const std::string& to_context_raw,
        const std::string& code,
        const std::string& message,
        const std::string& details_raw
    ) const {
        return "{"
               "\"kind\":\"response\","
               "\"ts\":" + json::quote(now_rfc3339()) + ","
               "\"response\":{"
               "\"intention_id\":" + json::quote(intention_id) + ","
               "\"to\":" + to_raw + ","
               "\"from\":{"
               "\"context\":" + json::quote(response_from_context(to_context_raw)) + ","
               "\"cap\":" + json::quote(cap) + ","
               "\"type\":" + json::quote(type) +
               "},"
               "\"identity\":{"
               "\"id\":" + json::quote(wrapper_name_) + ","
               "\"kind\":\"wrapper\""
               "},"
               "\"status\":\"error\","
               "\"error\":{"
               "\"origin\":\"wrapper\","
               "\"code\":" + json::quote(code) + ","
               "\"message\":" + json::quote(message) + ","
               "\"details\":" + details_raw +
               "}"
               "}"
               "}";
    }
};

}  // namespace wrapper
