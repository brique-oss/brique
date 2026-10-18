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
#include <algorithm>
#include <chrono>
#include <atomic>
#include <condition_variable>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <functional>
#include <memory>
#include <iostream>
#include <mutex>
#include <stdexcept>
#include <string>
#include <thread>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

#include "constants.hpp"
#include "http.hpp"
#include "json.hpp"
#include "scheduler.hpp"
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

inline std::size_t positive_env(const char* key, std::size_t fallback) {
    const std::string raw = env_or(key);
    if (raw.empty()) {
        return fallback;
    }
    std::size_t consumed = 0;
    const unsigned long value = std::stoul(raw, &consumed);
    if (consumed != raw.size() || value == 0) {
        throw std::runtime_error(std::string(key) + " must be a positive integer");
    }
    return static_cast<std::size_t>(value);
}

class ParamsView {
public:
    using Resolver = std::function<std::string(const std::string&)>;
    using Releaser = std::function<void(const std::string&)>;

    explicit ParamsView(std::string raw, Resolver resolver = {}, Releaser releaser = {})
        : raw_(std::move(raw)), resolver_(std::move(resolver)), releaser_(std::move(releaser)) {}
    ParamsView(const ParamsView&) = delete;
    ParamsView& operator=(const ParamsView&) = delete;
    ~ParamsView() {
        if (!releaser_) return;
        try {
            for (const auto& item : json::object_entries(raw_)) {
                if (consumed_.count(item.first) == 0 && item.first != "@matter") releaser_(item.second);
            }
        } catch (...) {}
    }

    std::string value_raw(const std::string& key, const std::string& fallback = "null") const {
        if (resolved_.count(key) != 0) return resolved_.at(key);
        const std::string raw_value = json::raw_field(raw_, key, fallback);
        const std::string value = resolver_ ? resolver_(raw_value) : raw_value;
        resolved_[key] = value; consumed_.insert(key); return value;
    }

    std::string string_value(const std::string& key, const std::string& fallback = "") const {
        const std::string value = value_raw(key, "");
        if (value.empty() || value.front() != '"') return fallback;
        try { return json::parse_string_literal(value, 0); } catch (...) { return fallback; }
    }

    bool has(const std::string& key) const {
        return json::has_key(raw_, key);
    }

    const std::string& raw() const {
        return raw_;
    }

private:
    std::string raw_;
    Resolver resolver_;
    Releaser releaser_;
    mutable std::unordered_map<std::string, std::string> resolved_;
    mutable std::unordered_set<std::string> consumed_;
};

struct TransitTarget {
    std::string context;
    std::string cap;
    std::string type = kTypeUser;
};

struct CapacityBinding {
    std::string relative_context;
    std::string name;
    std::function<std::string(const ParamsView&)> callback;
};

struct MatterBinding {
    std::string relative_context;
    std::string name;
    std::function<std::string()> read;
    std::function<void(const std::string&)> write;
    bool notify_on_write = false;
};

struct BindingsFragment {
    std::vector<CapacityBinding> capacities;
    std::vector<MatterBinding> matters;
    std::vector<std::function<void()>> shutdown;
};

struct OutboundParams {
    std::string to_context;
    std::string to_cap;
    std::string to_type = kTypeUser;
    std::string from_context;
    std::string from_cap;
    std::string from_type = kTypeExecution;
    std::string params_raw = "{}";
    std::string matters_raw = "[]";
    std::string root_intention_id;
    std::string parent_intention_id;
};

struct ExecutionContext {
    std::string intention_id;
    std::string root_intention_id;
    std::string relative_context;
    std::string capacity;
};

inline thread_local ExecutionContext current_execution;

class Runtime {
public:
    Runtime()
        : raw_ctx_dir_(env_or("BRIQUE_CTX_DIR")),
          ctx_dir_(raw_ctx_dir_.empty() ? std::filesystem::path{} : std::filesystem::absolute(raw_ctx_dir_)),
          ctx_id_(env_or("BRIQUE_CTX_ID")),
          wrapper_name_(env_or("BRIQUE_WRAPPER_NAME")),
          wrapper_src_dir_(std::filesystem::absolute(
              env_or("BRIQUE_WRAPPER_SRC_DIR", (ctx_dir_ / "code" / wrapper_name_).string()))),
          wrapper_build_dir_(std::filesystem::absolute(
              env_or("BRIQUE_WRAPPER_BUILD_DIR", (ctx_dir_ / "build" / wrapper_name_).string()))),
          wrapper_work_dir_(std::filesystem::absolute(
              env_or("BRIQUE_WORKDIR", (ctx_dir_ / "tmp" / ("wrapper_" + wrapper_name_)).string()))),
          transit_dir_(wrapper_work_dir_ / "transit"),
          transit_max_bytes_(positive_env("BRIQUE_TRANSIT_MAX_BYTES", 64U * 1024U * 1024U)),
          transit_ttl_seconds_(positive_env("BRIQUE_TRANSIT_TTL_SECONDS", 60U * 60U)),
          scheduler_(std::make_unique<WorkScheduler>(
              positive_env("BRIQUE_MAX_CONCURRENCY", 16),
              positive_env("BRIQUE_QUEUE_LIMIT", 256))) {
        allowed_context_root_ = ctx_dir_;
        std::size_t components = 0; bool in_component = false;
        for (char ch : ctx_id_) { if (ch == '/') in_component = false; else if (!in_component) { ++components; in_component = true; } }
        for (std::size_t i = 1; i < components; ++i) allowed_context_root_ = allowed_context_root_.parent_path();
    }

    ~Runtime() { stop_expiry_worker(); }

    void load(const BindingsFragment& fragment) {
        if (raw_ctx_dir_.empty() || !std::filesystem::path(raw_ctx_dir_).is_absolute() ||
            ctx_dir_.empty() || !std::filesystem::is_directory(ctx_dir_) ||
            !std::filesystem::is_regular_file(ctx_dir_ / "context.json")) {
            throw std::runtime_error("missing BRIQUE_CTX_DIR");
        }
        if (ctx_id_.empty() || ctx_id_.front() != '/') {
            throw std::runtime_error("BRIQUE_CTX_ID must be a non-empty absolute context path");
        }
        if (wrapper_name_.empty()) {
            throw std::runtime_error("missing BRIQUE_WRAPPER_NAME");
        }
        std::filesystem::create_directories(wrapper_work_dir_);
        std::filesystem::create_directories(transit_dir_);
        sweep_transit_files();
        for (const auto& binding : fragment.capacities) {
            if (binding.name.empty() || !binding.callback) {
                throw std::runtime_error("invalid capacity binding");
            }
            const std::string key = make_key(binding.relative_context, binding.name);
            if (capacity_bindings_.count(key) != 0) {
                throw std::runtime_error("duplicate capacity binding: " + binding.name);
            }
            capacity_bindings_[key] = binding.callback;
        }
        for (const auto& binding : fragment.matters) {
            if (binding.name.empty() || (!binding.read && !binding.write)) {
                throw std::runtime_error("invalid matter binding");
            }
            const std::string key = make_key(binding.relative_context, binding.name);
            if (matter_bindings_.count(key) != 0) {
                throw std::runtime_error("duplicate matter binding: " + binding.name);
            }
            matter_bindings_[key] = std::make_shared<MatterState>(binding);
        }
        for (const auto& hook : fragment.shutdown) {
            if (!hook) {
                throw std::runtime_error("invalid shutdown hook");
            }
        }
        shutdown_hooks_ = fragment.shutdown;
        transport_.connect(boundary_url());
        start_expiry_worker();
    }

    int run() {
        transport_.send_text(wrapper_ready_message());
        std::string raw;
        while (!stop_requested_ && transport_.receive_text(raw)) {
            route(raw);
        }
        scheduler_->close();
        stop_expiry_worker();
        for (const auto& hook : shutdown_hooks_) {
            try { hook(); } catch (const std::exception& ex) {
                std::cerr << "cpp wrapper shutdown hook failed: " << ex.what() << "\n";
            }
        }
        transport_.close();
        return 0;
    }

    const std::string& ctx_id() const { return ctx_id_; }
    const std::string& wrapper_name() const { return wrapper_name_; }

    void emit_intention(OutboundParams params) {
        transport_.send_text(outbound_message(params, false, new_id()));
    }

    std::string emit_and_wait(OutboundParams params) {
        const std::string id = new_id();
        auto pending = std::make_shared<Pending>();
        {
            std::lock_guard<std::mutex> lock(pending_mu_);
            if (pending_.count(id) != 0) throw std::runtime_error("duplicate pending intention id");
            pending_[id] = pending;
        }
        try {
            transport_.send_text(outbound_message(params, true, id));
            std::unique_lock<std::mutex> lock(pending->mu);
            std::size_t observed_activity = pending->activity;
            while (!pending->done) {
                const bool woke = pending->cv.wait_for(lock, std::chrono::seconds(30), [&] {
                    return pending->done || pending->activity != observed_activity;
                });
                if (!woke) throw std::runtime_error("outbound intention inactivity timeout");
                observed_activity = pending->activity;
            }
            if (json::string_field(pending->response, "status") != kStatusOk) {
                const std::string error = json::object_field(pending->response, "error", "{}");
                throw std::runtime_error(json::string_field(error, "message", "outbound intention failed"));
            }
            const std::string payload = json::object_field(pending->response, "payload", "{}");
            {
                std::lock_guard<std::mutex> pending_lock(pending_mu_);
                pending_.erase(id);
            }
            return payload;
        } catch (...) {
            std::lock_guard<std::mutex> lock(pending_mu_);
            pending_.erase(id);
            throw;
        }
    }

    // Emit selects inline, @object, @file or transient @matter from target.
    std::string emit(const std::string& value_raw, const TransitTarget& target) {
        if (is_transit_reference(value_raw) || (is_json_scalar(value_raw) && value_raw.size() <= 64U * 1024U)) return value_raw;
        if (same_wrapper(target)) {
            const std::string id = "obj_" + new_id();
            std::lock_guard<std::mutex> lock(transit_mu_); transit_objects_[id] = value_raw;
            return "{\"@object\":" + json::quote(id) + ",\"wrapper\":" + json::quote(wrapper_name_) + ",\"type\":\"\"}";
        }
        if (value_raw.size() <= 64U * 1024U) return value_raw;
        if (target.context.rfind("@ext_", 0) == 0 || value_raw.size() > transit_max_bytes_) return create_transient_matter(value_raw, "json", target);
        return write_transit_file(value_raw, "json");
    }

    // Call prepares every JSON parameter using the destination address.
    std::string call(const TransitTarget& target, const std::unordered_map<std::string, std::string>& params) {
        std::string raw = "{"; bool first = true;
        for (const auto& item : params) { if (!first) raw += ','; first = false; raw += json::quote(item.first) + ":" + emit(item.second, target); }
        raw += "}";
        OutboundParams outbound; outbound.to_context=target.context; outbound.to_cap=target.cap; outbound.to_type=target.type; outbound.params_raw=raw;
        return emit_and_wait(outbound);
    }

private:
    using CapacityFn = std::function<std::string(const ParamsView&)>;
    struct MatterState {
        MatterBinding binding;
        std::mutex mu;

        explicit MatterState(MatterBinding value) : binding(std::move(value)) {}
    };
    struct Subscription {
        std::string id;
        std::string matter_id;
        std::string context;
        std::string target_context;
        std::string target_cap;
        std::string target_type;
        std::string owner_context;
        std::string owner_cap;
        std::string owner_type;
    };
    struct Pending {
        std::mutex mu;
        std::condition_variable cv;
        std::size_t activity = 0;
        bool done = false;
        std::string response;
    };

    std::string raw_ctx_dir_;
    std::filesystem::path ctx_dir_;
    std::string ctx_id_;
    std::string wrapper_name_;
    std::filesystem::path wrapper_src_dir_;
    std::filesystem::path wrapper_build_dir_;
    std::filesystem::path wrapper_work_dir_;
    std::filesystem::path transit_dir_;
    std::filesystem::path allowed_context_root_;
    std::size_t transit_max_bytes_;
    std::size_t transit_ttl_seconds_;
    WSClient transport_;
    std::atomic<bool> stop_requested_{false};
    std::unique_ptr<WorkScheduler> scheduler_;
    std::unordered_map<std::string, CapacityFn> capacity_bindings_;
    std::unordered_map<std::string, std::shared_ptr<MatterState>> matter_bindings_;
    std::vector<std::function<void()>> shutdown_hooks_;
    std::mutex subscriptions_mu_;
    std::unordered_map<std::string, Subscription> subscriptions_;
    std::mutex pending_mu_;
    std::unordered_map<std::string, std::shared_ptr<Pending>> pending_;
    std::mutex transit_mu_;
    std::unordered_map<std::string, std::string> transit_objects_;
    struct ExpiringMatter { std::chrono::steady_clock::time_point at; std::string owner; std::string id; long long revision; long long size; };
    std::mutex expiry_mu_;
    std::condition_variable expiry_cv_;
    bool expiry_stop_ = false;
    std::vector<ExpiringMatter> expiring_matters_;
    std::thread expiry_thread_;

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
        const std::string kind = json::string_field(raw, "kind");
        if (kind == kKindResponse) {
            resolve_response(json::object_field(raw, "response", "{}"));
            return;
        }
        if (kind != kKindIntention) {
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
        if (to_type == kTypeMatter) {
            schedule_matter(intention_id, from, to, params_raw, to_cap);
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

        const std::string work_key = "user:" + rel_ctx + ":" + to_cap;
        const std::string to_context = json::string_field(to, "context");
        const std::string correlation_raw = json::object_field(intention, "correlation", "{}");
        const CapacityFn callback = it->second;
        const bool accepted = scheduler_->submit(work_key, [this, callback, params_raw, intention_id, from, to_cap, to_type, to_context, correlation_raw] {
            const ExecutionContext previous = current_execution;
            std::string root_id = json::string_field(correlation_raw, "root_intention_id");
            if (root_id.empty()) root_id = intention_id;
            current_execution = ExecutionContext{
                intention_id,
                root_id,
                target_context(to_context),
                to_cap,
            };
            try {
                const std::string payload = callback(ParamsView(
                    params_raw,
                    [this](const std::string& value) { return resolve_transit(value); },
                    [this](const std::string& value) { release_transit(value, true); }
                ));
                transport_.send_text(ok_response(intention_id, from, to_cap, to_type, to_context, payload));
            } catch (const std::exception& ex) {
                const std::string error_id = new_id();
                std::cerr << "cpp wrapper capacity error id=" << error_id << ": " << ex.what() << "\n";
                transport_.send_text(error_response(intention_id, from, to_cap, to_type, to_context,
                                                    "internal", "wrapper capacity failed",
                                                    "{\"reason\":\"wrapper_capacity_failed\",\"error_id\":" + json::quote(error_id) + "}"));
            }
            current_execution = previous;
        });
        if (!accepted) {
            transport_.send_text(error_response(intention_id, from, to_cap, to_type, to_context,
                                                "refused", "wrapper pending-work queue is full",
                                                "{\"reason\":\"busy\"}"));
        }
    }

    void resolve_response(const std::string& response) {
        const std::string id = json::string_field(response, "intention_id");
        std::shared_ptr<Pending> pending;
        {
            std::lock_guard<std::mutex> lock(pending_mu_);
            const auto it = pending_.find(id);
            if (it == pending_.end()) {
                std::cerr << "cpp wrapper orphan response: " << id << "\n";
                return;
            }
            pending = it->second;
        }
        {
            std::lock_guard<std::mutex> lock(pending->mu);
            if (json::string_field(response, "status") == "running") {
                ++pending->activity;
            } else {
                pending->response = response;
                pending->done = true;
            }
        }
        pending->cv.notify_all();
    }

    std::string outbound_message(OutboundParams params, bool await_response, const std::string& id) const {
        if (params.from_context.empty()) params.from_context = current_execution.relative_context;
        if (params.from_cap.empty()) params.from_cap = current_execution.capacity;
        if (params.root_intention_id.empty()) {
            params.root_intention_id = current_execution.root_intention_id.empty()
                ? current_execution.intention_id : current_execution.root_intention_id;
        }
        if (params.parent_intention_id.empty()) params.parent_intention_id = current_execution.intention_id;
        return "{\"kind\":\"intention\",\"ts\":" + json::quote(now_rfc3339()) +
            ",\"intention\":{\"intention_id\":" + json::quote(id) +
            ",\"await_response\":" + (await_response ? "true" : "false") +
            ",\"to\":{\"context\":" + json::quote(params.to_context) + ",\"cap\":" + json::quote(params.to_cap) +
            ",\"type\":" + json::quote(params.to_type) + "}," +
            "\"from\":{\"context\":" + json::quote(params.from_context) + ",\"cap\":" + json::quote(params.from_cap) +
            ",\"type\":" + json::quote(params.from_type) + "}," +
            "\"identity\":{\"id\":" + json::quote(wrapper_name_) + ",\"kind\":\"wrapper\"}," +
            "\"params\":" + params.params_raw + ",\"matters\":" + params.matters_raw + "," +
            "\"correlation\":{\"root_intention_id\":" + json::quote(params.root_intention_id) +
            ",\"parent_intention_id\":" + json::quote(params.parent_intention_id) + "}}}";
    }

    void schedule_matter(
        const std::string& intention_id,
        const std::string& from,
        const std::string& to,
        const std::string& params_raw,
        const std::string& cap
    ) {
        const std::string context_raw = json::string_field(to, "context");
        const std::string rel_ctx = target_context(context_raw);
        const std::string matter_id = json::string_field(params_raw, "matter_id");
        const std::string sub_id = json::string_field(params_raw, "sub_id");
        const std::string key = (cap == kCapMatterSubscribe || cap == kCapMatterUnsubscribe)
            ? "matter:subscription:" + sub_id
            : "matter:" + rel_ctx + ":" + matter_id;
        const bool accepted = scheduler_->submit(
            key,
            [this, intention_id, from, context_raw, rel_ctx, params_raw, cap, matter_id, sub_id] {
            try {
                handle_matter(intention_id, from, context_raw, rel_ctx, params_raw, cap, matter_id, sub_id);
            } catch (const std::exception& ex) {
                const std::string error_id = new_id();
                std::cerr << "cpp wrapper matter error id=" << error_id << ": " << ex.what() << "\n";
                transport_.send_text(error_response(intention_id, from, cap, kTypeMatter, context_raw,
                                                    "internal", "wrapper matter operation failed",
                                                    "{\"reason\":\"wrapper_matter_failed\",\"error_id\":" + json::quote(error_id) + "}"));
            }
            }
        );
        if (!accepted) {
            transport_.send_text(error_response(intention_id, from, cap, kTypeMatter, context_raw,
                                                "refused", "wrapper pending-work queue is full",
                                                "{\"reason\":\"busy\"}"));
        }
    }

    void handle_matter(
        const std::string& intention_id,
        const std::string& from,
        const std::string& context_raw,
        const std::string& rel_ctx,
        const std::string& params_raw,
        const std::string& cap,
        const std::string& matter_id,
        const std::string& sub_id
    ) {
        if (cap == kCapMatterSubscribe) {
            subscribe(intention_id, from, context_raw, rel_ctx, params_raw, matter_id, sub_id);
            return;
        }
        if (cap == kCapMatterUnsubscribe) {
            unsubscribe(intention_id, from, context_raw, matter_id, sub_id);
            return;
        }
        const auto it = matter_bindings_.find(make_key(rel_ctx, matter_id));
        if (it == matter_bindings_.end()) {
            transport_.send_text(error_response(intention_id, from, cap, kTypeMatter, context_raw,
                                                "not_found", "unknown wrapper matter",
                                                "{\"reason\":\"unknown_matter\"}"));
            return;
        }
        const auto state = it->second;
        if (cap == kCapMatterRead) {
            if (!state->binding.read) {
                throw std::runtime_error("matter is not readable");
            }
            std::string value;
            {
                std::lock_guard<std::mutex> lock(state->mu);
                value = state->binding.read();
            }
            const std::string payload = "{\"kind\":\"inline\",\"bytes\":" +
                json::quote(base64_encode(value)) + ",\"size\":" + std::to_string(value.size()) + "}";
            transport_.send_text(ok_response(intention_id, from, cap, kTypeMatter, context_raw, payload));
            return;
        }
        if (cap == kCapMatterWrite) {
            if (!state->binding.write) {
                throw std::runtime_error("matter is not writable");
            }
            std::string value = json::raw_field(params_raw, "value", "");
            if (value.empty()) value = json::raw_field(params_raw, "data", "");
            if (value.empty()) throw std::runtime_error("no writable value found in params");
            {
                std::lock_guard<std::mutex> lock(state->mu);
                state->binding.write(value);
            }
            const std::size_t notified = state->binding.notify_on_write
                ? notify_subscribers(rel_ctx, matter_id, value)
                : 0;
            const std::string payload = "{\"ok\":true,\"matter_id\":" + json::quote(matter_id) +
                ",\"substance_mode\":\"wrapper\",\"notified\":" + std::to_string(notified) + "}";
            transport_.send_text(ok_response(intention_id, from, cap, kTypeMatter, context_raw, payload));
            return;
        }
        transport_.send_text(error_response(intention_id, from, cap, kTypeMatter, context_raw,
                                            "invalid", "unsupported wrapper matter operation",
                                            "{\"reason\":\"unsupported_matter_cap\"}"));
    }

    void subscribe(
        const std::string& intention_id, const std::string& from, const std::string& context_raw,
        const std::string& rel_ctx, const std::string& params_raw,
        const std::string& matter_id, std::string sub_id
    ) {
        if (sub_id.empty()) sub_id = new_id();
        const std::string target_raw = json::object_field(params_raw, "target", from);
        Subscription sub{
            sub_id, matter_id, rel_ctx,
            json::string_field(target_raw, "context"), json::string_field(target_raw, "cap"), json::string_field(target_raw, "type"),
            json::string_field(from, "context"), json::string_field(from, "cap"), json::string_field(from, "type")
        };
        if (matter_id.empty() || sub.target_context.empty() || sub.target_cap.empty() || sub.target_type.empty()) {
            transport_.send_text(error_response(intention_id, from, kCapMatterSubscribe, kTypeMatter, context_raw,
                                                "invalid", "matter_id and a complete notification target are required",
                                                "{\"reason\":\"invalid_subscription\"}"));
            return;
        }
        {
            std::lock_guard<std::mutex> lock(subscriptions_mu_);
            const auto existing = subscriptions_.find(sub_id);
            if (existing != subscriptions_.end()) {
                const Subscription& old = existing->second;
                if (old.matter_id != sub.matter_id || old.context != sub.context ||
                    old.owner_context != sub.owner_context || old.owner_cap != sub.owner_cap || old.owner_type != sub.owner_type ||
                    old.target_context != sub.target_context || old.target_cap != sub.target_cap || old.target_type != sub.target_type) {
                    transport_.send_text(error_response(intention_id, from, kCapMatterSubscribe, kTypeMatter, context_raw,
                                                        "refused", "subscription identifier is already in use",
                                                        "{\"reason\":\"duplicate_sub_id\"}"));
                    return;
                }
            } else {
                subscriptions_[sub_id] = sub;
            }
        }
        const std::string payload = "{\"ok\":true,\"matter_id\":" + json::quote(matter_id) +
            ",\"sub_id\":" + json::quote(sub_id) + ",\"substance_mode\":\"wrapper\"}";
        transport_.send_text(ok_response(intention_id, from, kCapMatterSubscribe, kTypeMatter, context_raw, payload));
    }

    void unsubscribe(
        const std::string& intention_id, const std::string& from, const std::string& context_raw,
        const std::string& matter_id, const std::string& sub_id
    ) {
        if (matter_id.empty() || sub_id.empty()) {
            transport_.send_text(error_response(intention_id, from, kCapMatterUnsubscribe, kTypeMatter, context_raw,
                                                "invalid", "matter_id and sub_id are required",
                                                "{\"reason\":\"invalid_subscription\"}"));
            return;
        }
        bool removed = false;
        {
            std::lock_guard<std::mutex> lock(subscriptions_mu_);
            const auto it = subscriptions_.find(sub_id);
            if (it != subscriptions_.end()) {
                const Subscription& sub = it->second;
                if (sub.owner_context != json::string_field(from, "context") ||
                    sub.owner_cap != json::string_field(from, "cap") || sub.owner_type != json::string_field(from, "type")) {
                    transport_.send_text(error_response(intention_id, from, kCapMatterUnsubscribe, kTypeMatter, context_raw,
                                                        "refused", "subscription belongs to another caller",
                                                        "{\"reason\":\"subscription_owner_mismatch\"}"));
                    return;
                }
                subscriptions_.erase(it);
                removed = true;
            }
        }
        const std::string payload = "{\"ok\":true,\"matter_id\":" + json::quote(matter_id) +
            ",\"sub_id\":" + json::quote(sub_id) + ",\"removed\":" + (removed ? "true" : "false") +
            ",\"substance_mode\":\"wrapper\"}";
        transport_.send_text(ok_response(intention_id, from, kCapMatterUnsubscribe, kTypeMatter, context_raw, payload));
    }

    std::size_t notify_subscribers(const std::string& rel_ctx, const std::string& matter_id, const std::string& value_raw) {
        std::vector<Subscription> snapshot;
        {
            std::lock_guard<std::mutex> lock(subscriptions_mu_);
            for (const auto& item : subscriptions_) snapshot.push_back(item.second);
        }
        std::size_t notified = 0;
        for (const auto& sub : snapshot) {
            if (sub.matter_id != matter_id || trim_slashes(sub.context) != trim_slashes(rel_ctx)) continue;
            const std::string message = "{\"kind\":\"intention\",\"ts\":" + json::quote(now_rfc3339()) +
                ",\"intention\":{\"intention_id\":" + json::quote(new_id()) + ",\"await_response\":false," +
                "\"to\":{\"context\":" + json::quote(sub.target_context) + ",\"cap\":" + json::quote(sub.target_cap) +
                ",\"type\":" + json::quote(sub.target_type) + "}," +
                "\"from\":{\"context\":" + json::quote(trim_slashes(rel_ctx)) + ",\"cap\":\"matter_event\",\"type\":\"matter\"}," +
                "\"identity\":{\"id\":" + json::quote(wrapper_name_) + ",\"kind\":\"wrapper\"}," +
                "\"params\":{\"event\":\"matter_written\",\"op\":\"write\",\"matter_id\":" + json::quote(matter_id) +
                ",\"substance_mode\":\"wrapper\",\"value\":" + value_raw + "}," +
                "\"correlation\":{\"root_intention_id\":\"\",\"parent_intention_id\":\"\"}}}";
            try { transport_.send_text(message); ++notified; }
            catch (const std::exception& ex) { std::cerr << "cpp wrapper notification failed: " << ex.what() << "\n"; }
        }
        return notified;
    }

    bool same_wrapper(const TransitTarget& target) const {
        if (target.type != kTypeUser || target.context.rfind("@ext_", 0) == 0) return false;
        std::string rel = target_context(target.context);
        return capacity_bindings_.count(make_key(rel, target.cap)) != 0;
    }

    static bool is_json_scalar(const std::string& raw) {
        const std::size_t pos = json::skip_ws(raw, 0);
        return pos >= raw.size() || (raw[pos] != '{' && raw[pos] != '[');
    }

    static bool exact_reference(const std::string& raw, const std::unordered_set<std::string>& allowed, const std::string& marker) {
        const auto entries = json::object_entries(raw);
        if (entries.empty()) return false;
        bool found = false;
        for (const auto& item : entries) { if (allowed.count(item.first) == 0) return false; if (item.first == marker) found = true; }
        return found;
    }

    static bool is_file_reference(const std::string& raw) {
        return exact_reference(raw, {"@file","format"}, "@file");
    }

    static bool is_object_reference(const std::string& raw) {
        return exact_reference(raw, {"@object","wrapper","type"}, "@object");
    }

    static bool is_matter_reference(const std::string& raw) {
        return exact_reference(raw, {"@matter","context","owner","format","consume","revision","size"}, "@matter") && json::bool_field(raw,"consume");
    }

    static bool is_transit_reference(const std::string& raw) {
        return is_file_reference(raw) || is_object_reference(raw) || is_matter_reference(raw);
    }

    std::string write_transit_file(const std::string& data, const std::string& format) {
        const std::filesystem::path path = transit_dir_ / ("transit_" + new_id() + ".bin");
        const std::filesystem::path pending = path.string() + ".tmp";
        { std::ofstream out(pending, std::ios::binary | std::ios::trunc); if (!out) throw std::runtime_error("cannot create transit file"); out.write(data.data(), static_cast<std::streamsize>(data.size())); if (!out) throw std::runtime_error("cannot write transit file"); }
        std::filesystem::rename(pending, path);
        return "{\"@file\":" + json::quote(path.string()) + ",\"format\":" + json::quote(format) + "}";
    }

    std::string resolve_transit(const std::string& raw) {
        if (is_object_reference(raw)) {
            if (json::string_field(raw, "wrapper") != wrapper_name_) throw std::runtime_error("transit object belongs to another wrapper");
            const std::string id = json::string_field(raw, "@object"); std::lock_guard<std::mutex> lock(transit_mu_);
            const auto it = transit_objects_.find(id); if (it == transit_objects_.end()) throw std::runtime_error("transit object not found");
            const std::string value = it->second; transit_objects_.erase(it); return value;
        }
        if (is_file_reference(raw)) {
            const std::filesystem::path supplied = json::string_field(raw, "@file");
            if (std::filesystem::is_symlink(std::filesystem::symlink_status(supplied))) throw std::runtime_error("symbolic transit file is forbidden");
            const std::filesystem::path path = std::filesystem::weakly_canonical(supplied);
            const std::filesystem::path root = std::filesystem::weakly_canonical(allowed_context_root_);
            const std::string path_s=path.string(), root_s=root.string()+std::string(1,std::filesystem::path::preferred_separator);
            if (path_s.rfind(root_s,0)!=0 || !std::filesystem::is_regular_file(path)) throw std::runtime_error("invalid transit file");
            const std::string data=read_file(path.string());
            const std::string format=json::string_field(raw,"format");
            const std::string value = format=="json" ? data : json::quote(data);
            std::filesystem::remove(path); return value;
        }
        if (is_matter_reference(raw)) return consume_transient_matter(raw);
        return raw;
    }

    std::string lease_url(const std::string& handle) const {
        std::string base=json::string_field(handle,"base_url"); while(!base.empty()&&base.back()=='/')base.pop_back();
        return base+json::string_field(handle,"path")+"?tok="+http::url_encode(json::string_field(handle,"tok"));
    }

    std::string matter_call(const std::string& context,const std::string& cap,const std::string& params) {
        OutboundParams out; out.to_context=context; out.to_cap=cap; out.to_type=kTypeMatter; out.params_raw=params; return emit_and_wait(out);
    }

    static std::string destination_context(const std::string& raw) {
        if (raw.rfind("@ext_",0)!=0) return raw.empty()?"":("/"+trim_slashes(raw));
        const std::size_t colon=raw.find(':'); if(colon==std::string::npos||colon+1>=raw.size())return "/root"; return "/"+trim_slashes(raw.substr(colon+1));
    }

    std::string create_transient_matter(const std::string& data,const std::string& format,const TransitTarget& target) {
        const long long expires=static_cast<long long>(std::time(nullptr))+static_cast<long long>(transit_ttl_seconds_);
        const std::string descriptor="{\"brique\":{\"kind\":\"matter\",\"substance_mode\":\"brique\",\"substance_type\":\"binary\",\"substance_framing\":\"bytes\",\"transient\":true,\"transient_expires_at\":"+std::to_string(expires)+"},\"objective\":{},\"functional\":{},\"subjective\":{}}";
        const std::string created=matter_call(target.context,"matter.create","{\"matter\":"+descriptor+"}");
        const std::string id=json::string_field(created,"matter_id"); long long revision=json::integer_field(created,"revision");
        if(id.empty()||revision<1)throw std::runtime_error("matter.create returned incomplete transit identity");
        const long long created_revision=revision;
        try {
            const std::string opened=matter_call(target.context,"matter.write","{\"matter_id\":"+json::quote(id)+",\"expected_revision\":"+std::to_string(revision)+",\"http_data\":true}");
            const std::string handle=json::object_field(opened,"http_data",""); if(handle.empty())throw std::runtime_error("matter.write returned no upload lease");
            const std::string committed=http::request("PUT",lease_url(handle),data,1024U*1024U).body; revision=json::integer_field(committed,"revision"); if(revision<1)throw std::runtime_error("Matter upload returned no revision");
        } catch (...) {
            try { matter_call(target.context,"matter.delete","{\"matter_id\":"+json::quote(id)+",\"expected_revision\":"+std::to_string(created_revision)+"}"); } catch (...) {}
            throw;
        }
        schedule_expiry(target.context,id,revision,static_cast<long long>(data.size()));
        return "{\"@matter\":"+json::quote(id)+",\"context\":"+json::quote(destination_context(target.context))+",\"owner\":"+json::quote(target.context)+",\"format\":"+json::quote(format)+",\"consume\":true,\"revision\":"+std::to_string(revision)+",\"size\":"+std::to_string(data.size())+"}";
    }

    std::string consume_transient_matter(const std::string& ref) {
        const std::string context=json::string_field(ref,"context"),id=json::string_field(ref,"@matter");
        const long long revision=json::integer_field(ref,"revision"),size=json::integer_field(ref,"size");
        const std::string payload=matter_call(context,"matter.read","{\"matter_id\":"+json::quote(id)+",\"read_mode\":\"brique|data\"}");
        const std::string brique=json::object_field(payload,"brique","");
        if(brique.empty()||!json::bool_field(brique,"transient")||json::integer_field(brique,"revision")!=revision)throw std::runtime_error("Matter is not the expected transient carrier");
        const std::string data_info=json::object_field(payload,"data",""); if(data_info.empty())throw std::runtime_error("matter.read returned no transit data");
        std::string data; const std::string kind=json::string_field(data_info,"kind");
        if(kind=="inline"){data=json::string_field(data_info,"bytes");if(json::string_field(data_info,"encoding")=="base64")data=base64_decode(data);}
        else if(kind=="http"){const std::string handle=json::object_field(data_info,"http","");data=http::request("GET",lease_url(handle),"",static_cast<std::size_t>(size)+1).body;}
        else throw std::runtime_error("unsupported Matter data kind");
        if(static_cast<long long>(data.size())!=size)throw std::runtime_error("transient Matter size mismatch");
        const std::string value=json::string_field(ref,"format")=="json"?data:json::quote(data);
        matter_call(context,"matter.delete","{\"matter_id\":"+json::quote(id)+",\"expected_revision\":"+std::to_string(revision)+",\"expected_byte_size\":"+std::to_string(size)+"}");
        cancel_expiry(id,revision);
        return value;
    }

    void release_transit(const std::string& raw,bool received) {
        try {
            if(is_object_reference(raw)){std::lock_guard<std::mutex>lock(transit_mu_);transit_objects_.erase(json::string_field(raw,"@object"));return;}
            if(is_file_reference(raw)){std::filesystem::remove(json::string_field(raw,"@file"));return;}
            if(!is_matter_reference(raw))return;
            const std::string context=json::string_field(raw,received?"context":"owner"),id=json::string_field(raw,"@matter");
            const long long revision=json::integer_field(raw,"revision"),size=json::integer_field(raw,"size");
            if(received){const std::string payload=matter_call(context,"matter.read","{\"matter_id\":"+json::quote(id)+",\"read_mode\":\"brique\"}");const std::string b=json::object_field(payload,"brique","");if(!json::bool_field(b,"transient")||json::integer_field(b,"revision")!=revision)return;}
            matter_call(context,"matter.delete","{\"matter_id\":"+json::quote(id)+",\"expected_revision\":"+std::to_string(revision)+",\"expected_byte_size\":"+std::to_string(size)+"}");
            cancel_expiry(id,revision);
        } catch(const std::exception& ex){std::cerr<<"cpp transit cleanup failed: "<<ex.what()<<"\n";}
    }

    void sweep_transit_files() {
        const auto cutoff=std::filesystem::file_time_type::clock::now()-std::chrono::seconds(transit_ttl_seconds_);
        for(const auto& entry:std::filesystem::directory_iterator(transit_dir_)){if(entry.is_regular_file()&&entry.path().filename().string().rfind("transit_",0)==0&&entry.last_write_time()<cutoff){std::error_code ec;std::filesystem::remove(entry.path(),ec);}}
    }

    void start_expiry_worker() {
        std::lock_guard<std::mutex> lock(expiry_mu_);
        if (expiry_thread_.joinable()) return;
        expiry_stop_ = false;
        expiry_thread_ = std::thread([this] { expiry_loop(); });
    }

    void stop_expiry_worker() {
        { std::lock_guard<std::mutex> lock(expiry_mu_); expiry_stop_ = true; }
        expiry_cv_.notify_all();
        if (expiry_thread_.joinable() && expiry_thread_.get_id() != std::this_thread::get_id()) expiry_thread_.join();
    }

    void schedule_expiry(const std::string& owner,const std::string& id,long long revision,long long size) {
        if (transit_ttl_seconds_ == 0) return;
        { std::lock_guard<std::mutex> lock(expiry_mu_); expiring_matters_.push_back(ExpiringMatter{std::chrono::steady_clock::now()+std::chrono::seconds(transit_ttl_seconds_),owner,id,revision,size}); }
        expiry_cv_.notify_all();
    }

    void cancel_expiry(const std::string& id,long long revision) {
        std::lock_guard<std::mutex> lock(expiry_mu_);
        expiring_matters_.erase(std::remove_if(expiring_matters_.begin(),expiring_matters_.end(),[&](const auto& item){return item.id==id&&item.revision==revision;}),expiring_matters_.end());
        expiry_cv_.notify_all();
    }

    void expiry_loop() {
        while (true) {
            ExpiringMatter item;
            {
                std::unique_lock<std::mutex> lock(expiry_mu_);
                if (expiry_stop_) return;
                if (expiring_matters_.empty()) { expiry_cv_.wait(lock,[this]{return expiry_stop_||!expiring_matters_.empty();}); if(expiry_stop_)return; }
                auto earliest=std::min_element(expiring_matters_.begin(),expiring_matters_.end(),[](const auto& a,const auto& b){return a.at<b.at;});
                if (expiry_cv_.wait_until(lock,earliest->at)==std::cv_status::no_timeout) continue;
                earliest=std::min_element(expiring_matters_.begin(),expiring_matters_.end(),[](const auto& a,const auto& b){return a.at<b.at;});
                if(earliest->at>std::chrono::steady_clock::now())continue;
                item=*earliest; expiring_matters_.erase(earliest);
            }
            try { matter_call(item.owner,"matter.delete","{\"matter_id\":"+json::quote(item.id)+",\"expected_revision\":"+std::to_string(item.revision)+",\"expected_byte_size\":"+std::to_string(item.size)+"}"); }
            catch(const std::exception&) { /* already consumed or remote cleanup unavailable */ }
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
