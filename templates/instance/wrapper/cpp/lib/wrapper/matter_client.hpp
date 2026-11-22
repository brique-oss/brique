/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

#pragma once

// The semantic Matter client of a wrapper: read, write, append and guarded update of the Matters of a context.
//
// A capacity that keeps its state in Matters does the same few things: read a Matter whose substance is served inline or through a
// one-shot HTTP lease, write it guarded by expected_revision, append lines to a journal guarded by expected_line_count, and replay a
// read-compute-write that lost a race. This does them once. The rules are shared by the three runtimes and fixed by
// vectors/matter_client.json.
//
// Values are raw JSON text, as everywhere in this runtime. Errors are CallError (code, message, details) with reason() and the helpers
// error_kind / is_revision_mismatch / is_line_count_mismatch / is_busy / is_not_found.

#include <chrono>
#include <cmath>
#include <functional>
#include <optional>
#include <random>
#include <string>
#include <thread>
#include <vector>

#include "http.hpp"
#include "json.hpp"
#include "runtime.hpp"

namespace wrapper {

constexpr std::size_t kInlineAppendMax = 240 * 1024;   // matter.append takes 256 KiB inline; a bigger block goes through its HTTP lease

inline std::string error_kind(const CallError& error) { return error.kind(); }
inline bool is_revision_mismatch(const CallError& error) { return error.kind() == "revision_mismatch" || error.kind() == "line_count_mismatch"; }
inline bool is_line_count_mismatch(const CallError& error) { return error.kind() == "line_count_mismatch"; }
inline bool is_busy(const CallError& error) { return error.kind() == "busy"; }
inline bool is_not_found(const CallError& error) { return error.kind() == "not_found"; }
inline bool is_outcome_unknown(const CallError& error) { return error.outcome_unknown(); }

// A consistency failure: the Matter says something the client cannot use. details.cause names which.
inline CallError consistency(const std::string& message, const std::string& cause, const std::string& more = "") {
    return CallError("conflict", message,
                     "{\"reason\":\"consistency_failure\",\"cause\":" + json::quote(cause) + (more.empty() ? "" : "," + more) + "}");
}

inline std::string cause_of(const CallError& error) { return json::string_field(error.details(), "cause"); }

struct Substance {
    std::string kind;   // "inline" or "lease"
    std::string text;   // the text, for an inline substance
    std::string url;    // where to fetch it, for a lease
};

// The URL of a lease: the engine nests it under data.http (its descriptor documents the fields flat): both are accepted.
inline std::string matter_lease_url(const std::string& data_raw) {
    const std::string nested = json::object_field(data_raw, "http", "");
    const std::string& handle = nested.empty() ? data_raw : nested;
    std::string base = json::string_field(handle, "base_url");
    const std::string path = json::string_field(handle, "path");
    const std::string token = json::string_field(handle, "tok");
    if (base.empty() || path.empty() || token.empty()) throw consistency("the Matter lease is malformed", "malformed_lease");
    while (!base.empty() && base.back() == '/') base.pop_back();
    return base + path + "?tok=" + http::url_encode(token);
}

inline Substance classify_substance(const std::string& data_raw) {
    std::size_t first = data_raw.find_first_not_of(" \t\r\n");
    if (first == std::string::npos || data_raw[first] != '{') throw consistency("the Matter has no readable substance", "no_substance");
    if (json::string_field(data_raw, "kind") == "http") return Substance{"lease", "", matter_lease_url(data_raw)};
    const std::string bytes = json::raw_field(data_raw, "bytes", "");
    if (bytes.empty() || bytes.front() != '"') throw consistency("the Matter substance is not text", "not_text");
    return Substance{"inline", json::string_field(data_raw, "bytes"), ""};
}

// A revision is a number or its decimal string; anything else is 0.
inline long long revision_of(const std::string& brique_raw) {
    std::string value = json::raw_field(brique_raw, "revision", "");
    if (value.size() >= 2 && value.front() == '"' && value.back() == '"') value = value.substr(1, value.size() - 2);
    try {
        std::size_t used = 0;
        const long long parsed = std::stoll(value, &used);
        return used == value.size() ? parsed : 0;
    } catch (...) {
        return 0;
    }
}

// The JSON text without the whitespace outside its strings.
inline std::string compact_json(const std::string& raw) {
    std::string out;
    bool in_string = false, escape = false;
    for (char ch : raw) {
        if (in_string) {
            out.push_back(ch);
            if (escape) escape = false;
            else if (ch == '\\') escape = true;
            else if (ch == '"') in_string = false;
        } else if (ch == '"') {
            in_string = true;
            out.push_back(ch);
        } else if (ch != ' ' && ch != '\t' && ch != '\r' && ch != '\n') {
            out.push_back(ch);
        }
    }
    return out;
}

// The lines of a journal substance, each as raw JSON; blank lines are skipped.
inline std::vector<std::string> parse_lines(const std::string& text) {
    std::vector<std::string> out;
    std::size_t start = 0;
    while (start <= text.size()) {
        std::size_t end = text.find('\n', start);
        if (end == std::string::npos) end = text.size();
        const std::string line = text.substr(start, end - start);
        start = end + 1;
        if (line.find_first_not_of(" \t\r\n") == std::string::npos) continue;
        const std::size_t first = line.find_first_not_of(" \t\r\n");
        const std::size_t last = line.find_last_not_of(" \t\r\n");
        if (line[first] != '{' || line[last] != '}' || json::match_balanced(line, first, '{', '}') != last) {
            throw consistency("a journal line is not a JSON object", "invalid_line");
        }
        out.push_back(line.substr(first, last - first + 1));
    }
    return out;
}

// The block appended to a journal: one compact JSON document per line, each ending with LF.
inline std::string dump_lines(const std::vector<std::string>& lines) {
    std::string out;
    for (const std::string& line : lines) out += compact_json(line) + "\n";
    return out;
}

// The pause after a lost race, attempt number `attempt` (0 for the first): random up to a bound that doubles up to a cap.
inline double guarded_write_pause_ms(int attempt, double random, long long base_ms, long long cap_ms) {
    double bound = static_cast<double>(base_ms);
    for (int i = 0; i < attempt && i < 30; ++i) bound *= 2;
    return random * (bound < static_cast<double>(cap_ms) ? bound : static_cast<double>(cap_ms));
}

struct MatterView {
    std::string id;
    std::string content;      // the parsed substance, a JSON object, as raw text
    std::string descriptor;   // the functional section of the Matter element
    std::string brique;
    long long revision = 0;
};

struct LinesView {
    std::vector<std::string> lines;
    long long total_lines = 0;
    long long revision = 0;
    long long offset = 0;
};

struct HttpResult {
    long status = 0;
    std::string body;
};

// The Matters of one context, from inside a running capacity.
class MatterClient {
public:
    using Call = std::function<std::string(const std::string& context, const std::string& cap, const std::string& params_raw)>;
    using Http = std::function<HttpResult(const std::string& method, const std::string& url, const std::string& body)>;

    MatterClient(Call call, std::string context, Http transfer = nullptr)
        : call_(std::move(call)), context_(std::move(context)), http_(transfer ? std::move(transfer) : Http(real_http)) {}

    // The client of the Matters of `context` over the engine route of this runtime.
    static MatterClient over(Runtime& runtime, const std::string& context) {
        return MatterClient([&runtime](const std::string& where, const std::string& cap, const std::string& params) {
            return runtime.matter_call(where, cap, params);
        }, context);
    }

    // The parsed substance (a JSON object) with its revision: inline up to 256 KiB, through a one-shot HTTP lease beyond.
    MatterView read(const std::string& matter_id, const std::string& context = "") {
        for (long long i = 0; i < lease_retries(); ++i) {
            const std::string payload = call_(where(context), "matter.read",
                "{\"matter_id\":" + json::quote(matter_id) + ",\"read_mode\":\"functional|data|brique\"}");
            if (auto view = view_of(matter_id, payload)) return *view;
        }
        throw consistency("the substance kept changing while it was being fetched", "unstable", "\"matter_id\":" + json::quote(matter_id));
    }

    // Several Matters in ONE engine request, in the order given. A missing Matter throws the CallError of its answer (is_not_found), like read.
    std::vector<MatterView> read_many(const std::vector<std::string>& matter_ids, const std::string& context = "") {
        std::string ids = "[";
        for (std::size_t i = 0; i < matter_ids.size(); ++i) ids += (i == 0 ? "" : ",") + json::quote(matter_ids[i]);
        const std::string payload = call_(where(context), "matter.read_batch", "{\"matter_ids\":" + ids + "],\"read_mode\":\"functional|data|brique\"}");
        const std::vector<std::string> items = json::array_items(json::array_field(payload, "result", "[]"));
        if (items.size() != matter_ids.size()) throw consistency("the batch read answered another number of Matters than asked", "batch_size");
        std::vector<MatterView> views;
        for (std::size_t i = 0; i < items.size(); ++i) {
            if (!json::bool_field(items[i], "ok")) throw call_error_from(json::raw_field(items[i], "error", "null"));
            std::optional<MatterView> view = view_of(matter_ids[i], items[i]);
            views.push_back(view ? *view : read(matter_ids[i], context));      // a stale lease: read that Matter again
        }
        return views;
    }

    // The substance exactly as the text it is, nothing parsed.
    std::string read_text(const std::string& matter_id, const std::string& context = "") {
        for (long long i = 0; i < lease_retries(); ++i) {
            const std::string payload = call_(where(context), "matter.read", "{\"matter_id\":" + json::quote(matter_id) + ",\"read_mode\":\"data\"}");
            bool stale = false;
            std::string text = text_of(json::raw_field(payload, "data", ""), stale);
            if (!stale) return text;
        }
        throw consistency("the substance kept changing while it was being fetched", "unstable", "\"matter_id\":" + json::quote(matter_id));
    }

    bool exists(const std::string& matter_id, const std::string& context = "") {
        return json::bool_field(call_(where(context), "matter.exists", "{\"matter_id\":" + json::quote(matter_id) + "}"), "exist");
    }

    // The objective part of the meaning of a Matter (its name, its type), without reading its substance; "{}" when there is none.
    std::string objective(const std::string& matter_id, const std::string& context = "") {
        const std::string payload = call_(where(context), "matter.read", "{\"matter_id\":" + json::quote(matter_id) + ",\"read_mode\":\"meaning\"}");
        return json::object_field(json::object_field(payload, "meaning", "{}"), "objective", "{}");
    }

    // Create a Matter. With no content it is created with no substance (a journal: line-framed, empty).
    long long create(const std::string& matter_id, const std::string& descriptor_raw, const std::optional<std::string>& content_raw = std::nullopt) {
        std::string params = "{\"matter_id\":" + json::quote(matter_id) + ",\"matter\":" + descriptor_raw;
        if (content_raw) params += ",\"payload\":" + json::quote(*content_raw);
        return revision_of(call_(context_, "matter.create", params + "}"));
    }

    // One atomic write of the whole content, guarded by expected_revision (a revision is a nanosecond timestamp, above 2^53: its decimal
    // string is exact whatever the JSON number handling of the caller). Without a revision the write is not guarded.
    long long write(const std::string& matter_id, const std::string& content_raw, const std::optional<long long>& expected_revision = std::nullopt) {
        return revision_of(call_(context_, "matter.write", write_params(matter_id, content_raw, expected_revision)));
    }

    static std::string write_params(const std::string& matter_id, const std::string& content_raw, const std::optional<long long>& expected_revision) {
        std::string params = "{\"matter_id\":" + json::quote(matter_id) + ",\"data\":" + json::quote(content_raw);
        if (expected_revision) params += ",\"expected_revision\":" + json::quote(std::to_string(*expected_revision));
        return params + "}";
    }

    void remove(const std::string& matter_id) { call_(context_, "matter.delete", "{\"matter_id\":" + json::quote(matter_id) + "}"); }

    // (revision, total_lines) of a line-framed Matter, without reading its substance.
    std::pair<long long, long long> stat(const std::string& matter_id) {
        const std::string payload = call_(context_, "matter.stat", "{\"matter_id\":" + json::quote(matter_id) + "}");
        return {revision_of(payload), json::integer_field(payload, "total_lines", 0)};
    }

    // A range of lines (offset is 0-based): inline up to 256 KiB, through a one-shot HTTP lease beyond.
    LinesView read_lines(const std::string& matter_id, long long offset, long long length) {
        const std::string params = "{\"matter_id\":" + json::quote(matter_id) + ",\"read_mode\":\"data|brique\",\"data_range\":{\"unit\":\"lines\",\"offset\":" +
                                   std::to_string(offset) + ",\"length\":" + std::to_string(length) + "}}";
        for (long long i = 0; i < lease_retries(); ++i) {
            const std::string payload = call_(context_, "matter.read", params);
            const std::string data = json::raw_field(payload, "data", "");
            bool stale = false;
            const std::string text = text_of(data, stale);
            if (stale) continue;
            const std::string brique = json::object_field(payload, "brique", "{}");
            const long long total = json::has_key(data, "total_lines") ? json::integer_field(data, "total_lines", 0)
                                                                        : json::integer_field(brique, "substance_line_count", 0);
            return LinesView{parse_lines(text), total, revision_of(brique), offset};
        }
        throw consistency("the substance kept changing while it was being fetched", "unstable", "\"matter_id\":" + json::quote(matter_id));
    }

    // Append a block of lines (raw JSON objects) in one atomic commit, guarded by expected_line_count; returns the new total of lines
    // (CallError with is_line_count_mismatch when the journal moved on). A block above kInlineAppendMax goes through the engine's HTTP lease.
    long long append_lines(const std::string& matter_id, const std::vector<std::string>& lines, long long expected_line_count) {
        const std::string text = dump_lines(lines);
        std::string params = "{\"matter_id\":" + json::quote(matter_id) + ",\"expected_line_count\":" + std::to_string(expected_line_count);
        if (text.size() <= kInlineAppendMax) {
            const std::string payload = call_(context_, "matter.append", params + ",\"data\":" + json::quote(text) + "}");
            return json::has_key(payload, "total_lines") ? json::integer_field(payload, "total_lines", 0)
                                                          : expected_line_count + static_cast<long long>(lines.size());
        }
        const std::string payload = call_(context_, "matter.append", params + ",\"http_data\":true}");
        const HttpResult put = http_("PUT", matter_lease_url(json::object_field(payload, "http_data", "{}")), text);
        if (put.status == 409) {
            throw CallError("refused", "the journal moved on while the append lease was in use", "{\"reason\":\"line_count_mismatch\"}");
        }
        if (put.status < 200 || put.status >= 300) {
            throw consistency("the append lease was refused (HTTP " + std::to_string(put.status) + ")", "lease_refused", "\"status\":" + std::to_string(put.status));
        }
        return expected_line_count + static_cast<long long>(lines.size());
    }

    // Read, recompute, write guarded by expected_revision, and start again after a short random pause when another writer got there first.
    // `compute` gets the content and returns the new content, or nothing when there is nothing to write (the update is already done); it
    // may put a result in `result`. Returns true when something was written. Throws ConcurrencyExhausted after BRIQUE_CAS_ATTEMPTS lost races.
    // Run it inside a CriticalSection when the writers of this process should take turns instead of racing.
    bool update(const std::string& matter_id,
                const std::function<std::optional<std::string>(const std::string& content, std::string& result)>& compute,
                std::string* result = nullptr) {
        const long long attempts = setting_value("BRIQUE_CAS_ATTEMPTS");
        static thread_local std::mt19937_64 generator{std::random_device{}()};
        for (long long attempt = 0; attempt < attempts; ++attempt) {
            const MatterView view = read(matter_id);
            std::string computed;
            const std::optional<std::string> next = compute(view.content, computed);
            if (result != nullptr) *result = computed;
            if (!next) return false;
            try {
                write(matter_id, *next, view.revision);
                return true;
            } catch (const CallError& refused) {
                if (!is_revision_mismatch(refused)) throw;
            }
            const double random = std::uniform_real_distribution<double>(0.0, 1.0)(generator);
            std::this_thread::sleep_for(std::chrono::duration<double, std::milli>(guarded_write_pause_ms(
                static_cast<int>(attempt), random, setting_value("BRIQUE_CAS_PAUSE_BASE_MS"), setting_value("BRIQUE_CAS_PAUSE_CAP_MS"))));
        }
        throw CallError("conflict", "revision conflicts on " + matter_id + " exhausted " + std::to_string(attempts) + " attempts",
                        "{\"reason\":\"concurrency_failure\",\"matter_id\":" + json::quote(matter_id) + "}");
    }

private:
    static long long lease_retries() { return setting_value("BRIQUE_LEASE_RETRIES"); }
    const std::string& where(const std::string& context) const { return context.empty() ? context_ : context; }

    static HttpResult real_http(const std::string& method, const std::string& url, const std::string& body) {
        const http::Response response = http::request(method, url, body, 128U * 1024U * 1024U, false, static_cast<long>((setting_value("BRIQUE_LEASE_TIMEOUT_MS") + 999) / 1000));
        return HttpResult{response.status, response.body};
    }

    // The text of a substance; `stale` is set when its lease went stale (the caller reads the Matter again).
    std::string text_of(const std::string& data_raw, bool& stale) {
        const Substance found = classify_substance(data_raw);
        if (found.kind == "inline") return found.text;
        HttpResult got;
        try {
            got = http_("GET", found.url, "");
        } catch (const std::exception& ex) {
            throw consistency(std::string("the substance lease could not be fetched: ") + ex.what(), "lease_unreachable");
        }
        if (got.status == 409) {
            stale = true;
            return "";
        }
        if (got.status < 200 || got.status >= 300) {
            throw consistency("the substance lease was refused (HTTP " + std::to_string(got.status) + ")", "lease_refused", "\"status\":" + std::to_string(got.status));
        }
        return got.body;
    }

    std::optional<MatterView> view_of(const std::string& matter_id, const std::string& payload) {
        bool stale = false;
        const std::string text = text_of(json::raw_field(payload, "data", ""), stale);
        if (stale) return std::nullopt;
        const std::size_t first = text.find_first_not_of(" \t\r\n");
        if (first == std::string::npos) throw consistency("the Matter substance is empty", "empty", "\"matter_id\":" + json::quote(matter_id));
        if (text[first] != '{') throw consistency("the Matter substance is not a JSON object", "not_object", "\"matter_id\":" + json::quote(matter_id));
        const std::string brique = json::object_field(payload, "brique", "{}");
        return MatterView{matter_id, text, json::object_field(payload, "functional", "{}"), brique, revision_of(brique)};
    }

    Call call_;
    std::string context_;
    Http http_;
};

}  // namespace wrapper
