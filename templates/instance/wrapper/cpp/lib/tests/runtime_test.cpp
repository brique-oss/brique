// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

#include <cstdlib>
#include <fstream>
#include <iostream>
#include <sstream>
#include <atomic>
#include <cassert>
#include <chrono>
#include <condition_variable>
#include <future>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#include "../wrapper/json.hpp"
#include "../wrapper/matter_client.hpp"
#include "../wrapper/runtime.hpp"
#include "../wrapper/scheduler.hpp"

// The shared corpus of wrapper frames (templates/instance/wrapper/vectors): this
// runtime must agree with the engine and with the Go and Python runtimes.
static void run_frame_vectors() {
    const char* override_path = std::getenv("BRIQUE_FRAME_VECTORS");
    // Run from cpp/lib (as the build notes say) or from cpp/lib/tests.
    std::ifstream input(override_path != nullptr ? override_path : "../../vectors/wrapper_frames.json");
    if (!input.good()) input.open("../../../vectors/wrapper_frames.json");
    assert(input.good());
    std::stringstream buffer;
    buffer << input.rdbuf();
    const std::string corpus = buffer.str();
    const auto cases = wrapper::json::array_items(wrapper::json::array_field(corpus, "cases"));
    assert(!cases.empty());
    for (const auto& item : cases) {
        const std::string name = wrapper::json::string_field(item, "name");
        const std::string raw = wrapper::json::string_field(item, "raw");
        const bool accept = wrapper::json::bool_field(item, "accept");
        std::string message;
        std::string delegation;
        const bool accepted = wrapper::Runtime::decode_wire_message(raw, message, delegation);
        if (accepted != accept) {
            std::cerr << "frame vector disagrees: " << name << " (accepted=" << accepted << ")\n";
            std::abort();
        }
        if (accepted) {
            if (wrapper::json::string_field(message, "kind") != wrapper::json::string_field(item, "message_kind") ||
                delegation != wrapper::json::string_field(item, "delegation")) {
                std::cerr << "frame vector extracts differently: " << name << "\n";
                std::abort();
            }
        }
    }
}

// The shared corpus of list parameters (templates/instance/wrapper/vectors/transit_lists.json): this runtime must agree with the
// Python and Go ones. Run from cpp/lib (as the build notes say) or from cpp/lib/tests.
namespace listvec {
using wrapper::json::array_field;
using wrapper::json::array_items;
using wrapper::json::has_key;
using wrapper::json::integer_field;
using wrapper::json::object_entries;
using wrapper::json::object_field;
using wrapper::json::raw_field;
using wrapper::json::string_field;

// top_has: the key is a field of the object itself (has_key looks at every depth).
static bool top_has(const std::string& raw, const std::string& key) {
    for (const auto& entry : object_entries(raw)) if (entry.first == key) return true;
    return false;
}

[[noreturn]] static void fail(const std::string& what) {
    std::cerr << "list vector disagrees: " << what << "\n";
    std::abort();
}

static std::string join(const std::vector<std::string>& items) {
    std::string out = "[";
    for (std::size_t i = 0; i < items.size(); ++i) out += (i == 0 ? "" : ",") + items[i];
    return out + "]";
}

// compact removes the whitespace outside strings, so two renderings of the same JSON compare equal.
static std::string compact(const std::string& raw) {
    std::string out; bool in_string = false, escaped = false;
    for (char c : raw) {
        if (in_string) { out += c; if (escaped) escaped = false; else if (c == '\\') escaped = true; else if (c == '"') in_string = false; continue; }
        if (c == '"') { in_string = true; out += c; continue; }
        if (!std::isspace(static_cast<unsigned char>(c))) out += c;
    }
    return out;
}

// make builds a value from its spec (see the corpus).
static std::string make(const std::string& spec) {
    const std::size_t p = wrapper::json::skip_ws(spec, 0);
    if (spec[p] == '[') { std::vector<std::string> out; for (const auto& item : array_items(spec)) out.push_back(make(item)); return join(out); }
    if (spec[p] == '{') {
        if (top_has(spec, "text")) return wrapper::json::quote(std::string(static_cast<std::size_t>(integer_field(spec, "text")), 'x'));
        if (top_has(spec, "object")) return "{\"k\":" + wrapper::json::quote(std::string(static_cast<std::size_t>(integer_field(spec, "object")), 'x')) + "}";
        if (top_has(spec, "list")) return make(array_field(spec, "list"));
        if (top_has(spec, "repeat")) {
            const std::string one = make(raw_field(spec, "repeat"));
            return join(std::vector<std::string>(static_cast<std::size_t>(integer_field(spec, "times")), one));
        }
    }
    return spec;
}

static std::string kind_of(const std::string& element) {
    if (element.empty() || element[0] != '{') return "inline";
    if (top_has(element, "@file")) return "file";
    if (top_has(element, "@object")) return "object";
    if (top_has(element, "@matter")) return "matter";
    return "inline";
}

static std::vector<std::string> strings_of(const std::string& array_raw) {
    std::vector<std::string> out;
    for (const auto& item : array_items(array_raw)) out.push_back(wrapper::json::parse_string_literal(item, 0));
    return out;
}

static std::string ref_text(const wrapper::MatterRef& ref) { return ref.context + "/" + ref.id; }

static void run() {
    const char* override_path = std::getenv("BRIQUE_TRANSIT_LIST_VECTORS");
    std::ifstream input(override_path != nullptr ? override_path : "../../vectors/transit_lists.json");
    if (!input.good()) input.open("../../../vectors/transit_lists.json");
    assert(input.good());
    std::stringstream buffer;
    buffer << input.rdbuf();
    const std::string corpus = buffer.str();
    if (integer_field(corpus, "inline_limit_bytes") != 64 * 1024) fail("the corpus inline limit is not this runtime's");

    const std::filesystem::path dir = std::filesystem::temp_directory_path() / ("brique_cpp_list_vectors_" + wrapper::new_id());
    std::filesystem::create_directories(dir);
    { std::ofstream(dir / "context.json") << "{}"; }
    setenv("BRIQUE_CTX_DIR", dir.c_str(), 1);
    setenv("BRIQUE_CTX_ID", "/root", 1);
    setenv("BRIQUE_WRAPPER_NAME", "test", 1);
    wrapper::Runtime runtime;
    wrapper::BindingsFragment fragment;
    fragment.capacities.push_back(wrapper::CapacityBinding{"", "consume", [](const wrapper::ParamsView&) { return std::string("{}"); }, false});
    fragment.capacities.push_back(wrapper::CapacityBinding{"child", "consume", [](const wrapper::ParamsView&) { return std::string("{}"); }, false});
    runtime.register_bindings(fragment);
    int created = 0;
    runtime.matter_call_hook = [&](const std::string&, const std::string& cap, const std::string&) {
        if (cap == "matter.create") return "{\"matter_id\":\"transit-" + std::to_string(++created) + "\",\"revision\":1}";
        if (cap == "matter.write") return std::string("{\"http_data\":{\"base_url\":\"http://matter.invalid\",\"path\":\"/upload\",\"tok\":\"secret\"}}");
        return std::string("{\"ok\":true}");
    };
    runtime.http_hook = [](const std::string&, const std::string&, const std::string&) { return std::string("{\"revision\":2}"); };
    const auto clean = [&] {
        for (const auto& entry : std::filesystem::directory_iterator(dir / "tmp" / "wrapper_test" / "transit")) std::filesystem::remove(entry.path());
    };

    {
        // locality: a small list is posted as an object exactly when the target is a capacity bound in this wrapper
        const std::string locality = object_field(corpus, "locality");
        for (const auto& item : array_items(array_field(locality, "cases"))) {
            const std::string context = string_field(item, "context"), cap = string_field(item, "cap");
            const std::string out = runtime.emit("[1]", wrapper::TransitTarget{context, cap, "user"});
            const bool local = kind_of(out) == "object";
            if (local != wrapper::json::bool_field(item, "local")) fail("locality of: " + context + " " + cap);
            runtime.release(out, false);
        }
    }

    for (const auto& item : array_items(array_field(corpus, "emit"))) {
        const std::string name = string_field(item, "name"), target_kind = string_field(item, "target");
        wrapper::TransitTarget target{"/root", "consume", "user"};                                   // a capacity bound in this wrapper
        if (target_kind == "instance") target = wrapper::TransitTarget{"child", "elsewhere", "user"};      // not bound here
        if (target_kind == "external") target = wrapper::TransitTarget{"@ext_peer:/root/work", "consume", "user"};
        const std::string out = runtime.emit(make(raw_field(item, "list")), target);
        const std::string expect = object_field(item, "expect");
        std::string form = kind_of(out);
        std::vector<std::string> elements;
        if (form == "inline" && !out.empty() && out[0] == '[') {
            for (const auto& element : array_items(out)) { elements.push_back(kind_of(element)); if (elements.back() != "inline") form = "elements"; }
        }
        if (form != string_field(expect, "form")) fail("emit form of: " + name + " (got " + form + ")");
        if (form == "elements") {
            std::vector<std::string> wanted = strings_of(array_field(expect, "elements"));
            if (elements != wanted) fail("emit elements of: " + name);
        }
        runtime.release(out, false);                      // what the emission posted and nobody took, as the producer would
        clean();
    }

    for (const auto& item : array_items(array_field(corpus, "receive"))) {
        const std::string name = string_field(item, "name");
        std::vector<std::string> raw; std::size_t files = 0, objects = 0;
        for (const auto& element : array_items(array_field(item, "elements"))) {
            if (top_has(element, "file")) { raw.push_back(runtime.post_file(raw_field(element, "file"))); ++files; }
            else if (top_has(element, "object")) { raw.push_back(runtime.post_object(raw_field(element, "object"))); ++objects; }
            else raw.push_back(raw_field(element, "plain"));
        }
        {
            wrapper::ParamsView view("{\"parts\":" + join(raw) + "}", [&](const std::string& v) { return runtime.resolve(v); },
                                     [&](const std::string& v) { runtime.release(v, true); });
            const std::string wanted = compact(array_field(item, "expect"));
            if (compact(view.value_raw("parts")) != wanted) fail("receive of: " + name + " (got " + view.value_raw("parts") + ")");
            if (compact(view.value_raw("parts")) != wanted) fail("receive, memoized, of: " + name);
        }
        if (runtime.transit_files() != 0 || runtime.posted_objects() != 0) fail("references left behind by: " + name);
        if (top_has(item, "consumed")) {
            const std::string consumed = object_field(item, "consumed");
            if (static_cast<std::size_t>(integer_field(consumed, "files")) != files || static_cast<std::size_t>(integer_field(consumed, "objects")) != objects)
                fail("the consumed counts of: " + name);
        }
    }

    for (const auto& item : array_items(array_field(corpus, "incoming_matters"))) {
        const std::string name = string_field(item, "name");
        wrapper::ParamsView view("{}", {}, {}, array_field(item, "matters"));
        for (const auto& entry : object_entries(object_field(item, "expect"))) {
            std::vector<std::string> got;
            for (const auto& ref : view.matters(entry.first)) got.push_back(ref_text(ref));
            if (got != strings_of(entry.second)) fail("incoming Matters of: " + name + " / " + entry.first);
        }
        if (top_has(item, "single")) {
            for (const auto& entry : object_entries(object_field(item, "single"))) {
                const std::string wanted = wrapper::json::parse_string_literal(entry.second, 0);
                if (wanted == "type_mismatch") {
                    bool mismatch = false;
                    try { view.matter(entry.first); } catch (const wrapper::TransitError& e) { mismatch = e.reason() == "type_mismatch"; }
                    if (!mismatch) fail("matter() on several Matters of: " + name);
                } else if (ref_text(view.matter(entry.first)) != wanted) {
                    fail("matter() of: " + name);
                }
            }
        }
        if (top_has(item, "absent")) {
            for (const auto& absent : strings_of(array_field(item, "absent"))) {
                bool missing = false;
                try { view.matters(absent); } catch (const wrapper::TransitError& e) { missing = e.reason() == "not_found"; }
                if (!missing) fail("an absent name is not_found in: " + name);
            }
        }
    }

    for (const auto& item : array_items(array_field(corpus, "outgoing_matters"))) {
        const std::string name = string_field(item, "name");
        if (top_has(item, "unrepresentable_in") && array_field(item, "unrepresentable_in").find("\"cpp\"") != std::string::npos) continue;
        std::unordered_map<std::string, std::vector<wrapper::MatterRef>> matters;
        std::size_t plain = 0;
        const auto ref_of = [](const std::string& raw) {
            const std::string text = string_field(raw, "matter");
            const std::size_t cut = text.rfind('/');
            return wrapper::MatterRef{text.substr(0, cut), text.substr(cut + 1), ""};
        };
        for (const auto& param : object_entries(object_field(item, "params"))) {
            if (param.second[0] == '{' && top_has(param.second, "matter")) matters[param.first].push_back(ref_of(param.second));
            else if (param.second[0] == '[' && !array_items(param.second).empty() && top_has(array_items(param.second)[0], "matter"))
                for (const auto& element : array_items(param.second)) matters[param.first].push_back(ref_of(element));
            else ++plain;
        }
        const std::string array = wrapper::Runtime::matters_array(matters);
        std::unordered_map<std::string, std::vector<std::string>> grouped;
        for (const auto& entry : array_items(array)) grouped[string_field(entry, "repr")].push_back(string_field(entry, "context") + "/" + string_field(entry, "id"));
        const std::string expect = object_field(item, "expect");
        const auto wanted = object_entries(object_field(expect, "matters"));
        if (grouped.size() != wanted.size()) fail("outgoing Matters of: " + name);
        for (const auto& entry : wanted) if (grouped[entry.first] != strings_of(entry.second)) fail("outgoing Matters order of: " + name);
        if (plain != array_items(array_field(expect, "params")).size()) fail("outgoing parameters of: " + name);
    }
    std::filesystem::remove_all(dir);
}
}  // namespace listvec

// The shared corpus of the busy retry (vectors/busy_retry.json): this runtime must agree with the Python and Go ones.
namespace busyvec {
using wrapper::json::array_field;
using wrapper::json::array_items;
using wrapper::json::string_field;

static std::string read_corpus() {
    std::ifstream input("../../vectors/busy_retry.json");
    if (!input.good()) input.open("../../../vectors/busy_retry.json");
    assert(input.good());
    std::stringstream buffer;
    buffer << input.rdbuf();
    return buffer.str();
}

static void run() {
    const std::string corpus = read_corpus();
    for (const auto& item : array_items(array_field(corpus, "is_busy"))) {
        const bool expected = wrapper::json::bool_field(item, "busy");
        const std::string response = wrapper::json::object_field(item, "response", "{}");
        if (wrapper::is_busy_response(response) != expected) {
            std::cerr << "busy vector disagrees: " << string_field(item, "name") << "\n";
            std::abort();
        }
    }
    for (const auto& item : array_items(array_field(corpus, "pause"))) {
        const int attempt = static_cast<int>(wrapper::json::integer_field(item, "attempt"));
        const double low = wrapper::busy_pause_ms(attempt, 0.0);
        const double high = wrapper::busy_pause_ms(attempt, 1.0);
        if (low != static_cast<double>(wrapper::json::integer_field(item, "min_ms")) ||
            high != static_cast<double>(wrapper::json::integer_field(item, "max_ms"))) {
            std::cerr << "busy pause disagrees for attempt " << attempt << ": " << low << ".." << high << "\n";
            std::abort();
        }
    }
    for (const auto& item : array_items(array_field(corpus, "settings"))) {
        ::unsetenv("BRIQUE_BUSY_ATTEMPTS");
        const std::string env = wrapper::json::object_field(item, "env", "{}");
        const std::string value = string_field(env, "BRIQUE_BUSY_ATTEMPTS");
        if (!value.empty()) ::setenv("BRIQUE_BUSY_ATTEMPTS", value.c_str(), 1);
        if (wrapper::busy_attempts() != wrapper::json::integer_field(item, "attempts")) {
            std::cerr << "busy attempts disagree for env " << env << "\n";
            std::abort();
        }
    }
    ::unsetenv("BRIQUE_BUSY_ATTEMPTS");
}
}  // namespace busyvec

// Critical sections: waiting for a taken key gives the execution slot back, and the keys are taken in sorted order.
static void run_critical_sections() {
    {
        wrapper::WorkScheduler scheduler(1, 8);
        std::mutex mu;
        std::condition_variable cv;
        bool holder_in = false, holder_go = false;
        std::vector<std::string> order;
        scheduler.submit("a", [&] {
            wrapper::CriticalSection section(scheduler, {"k"});
            { std::lock_guard<std::mutex> lock(mu); holder_in = true; }
            cv.notify_all();
            {
                wrapper::SlotYield own_call;   // a call of its own: it gives its slot back while it waits for the answer
                std::unique_lock<std::mutex> lock(mu);
                cv.wait(lock, [&] { return holder_go; });
            }
            std::lock_guard<std::mutex> lock(mu);
            order.push_back("holder done");
        }, false);
        { std::unique_lock<std::mutex> lock(mu); cv.wait(lock, [&] { return holder_in; }); }
        scheduler.submit("b", [&] {
            wrapper::CriticalSection section(scheduler, {"k"});
            std::lock_guard<std::mutex> lock(mu);
            order.push_back("waiter in");
        }, false);
        std::this_thread::sleep_for(std::chrono::milliseconds(50));
        { std::lock_guard<std::mutex> lock(mu); holder_go = true; }
        cv.notify_all();
        scheduler.close();
        assert((order == std::vector<std::string>{"holder done", "waiter in"}));
    }
    {
        wrapper::WorkScheduler scheduler(8, 64);
        std::atomic<int> done{0};
        for (int i = 0; i < 50; ++i) {
            const std::vector<std::string> keys = i % 2 == 0 ? std::vector<std::string>{"x", "y"} : std::vector<std::string>{"y", "x"};
            scheduler.submit("w" + std::to_string(i), [&, keys] { wrapper::CriticalSection section(scheduler, keys); ++done; }, false);
        }
        scheduler.close();
        assert(done == 50);
    }
}

// The state of the execution slots, as the diagnostic on demand writes it.
static void run_stats_and_dump() {
    wrapper::WorkScheduler scheduler(1, 1);
    std::mutex mu;
    std::condition_variable cv;
    bool go = false;
    auto hold = [&] { std::unique_lock<std::mutex> lock(mu); cv.wait(lock, [&] { return go; }); };
    assert(scheduler.submit("a", hold, true));
    assert(scheduler.submit("b", hold, false));
    assert(!scheduler.submit("c", hold, false));
    std::this_thread::sleep_for(std::chrono::milliseconds(50));
    const wrapper::SchedulerStats stats = scheduler.stats();
    assert(stats.free_slots == 0 && stats.active == 1 && stats.pending == 2 && stats.queued == 1 && stats.refused == 1 && stats.ordered_chains == 1);
    const std::string path = (std::filesystem::temp_directory_path() / "brique_cpp_state_test.txt").string();
    wrapper::dump_state(scheduler, path);
    std::ifstream in(path);
    std::stringstream text;
    text << in.rdbuf();
    assert(text.str().find("slots free 0 of 1") != std::string::npos);
    std::filesystem::remove(path);
    { std::lock_guard<std::mutex> lock(mu); go = true; }
    cv.notify_all();
    scheduler.close();
}

// The shared corpus of the Matter client (vectors/matter_client.json) and its guarded operations over scripted engine answers.
namespace mattervec {
using wrapper::json::array_field;
using wrapper::json::array_items;
using wrapper::json::object_field;
using wrapper::json::string_field;

static void fail(const std::string& what) {
    std::cerr << "matter client vector disagrees: " << what << "\n";
    std::abort();
}

static std::string read_corpus() {
    std::ifstream input("../../vectors/matter_client.json");
    if (!input.good()) input.open("../../../vectors/matter_client.json");
    assert(input.good());
    std::stringstream buffer;
    buffer << input.rdbuf();
    return buffer.str();
}

static bool unrepresentable(const std::string& item) {
    return array_field(item, "unrepresentable_in", "[]").find("\"cpp\"") != std::string::npos;
}

// Run `body`; the CallError it throws must have the cause `want` (nothing thrown is a failure).
template <typename Body>
static void expect_cause(const std::string& name, const std::string& want, Body body) {
    try {
        body();
    } catch (const wrapper::CallError& error) {
        if (wrapper::cause_of(error) != want) fail(name + ": cause " + wrapper::cause_of(error) + ", want " + want);
        return;
    }
    fail(name + ": no error, want " + want);
}

struct Script {
    std::vector<std::string> answers;   // payloads, or "!" + the details of an error
    std::vector<std::string> caps, params, contexts;
    std::string call(const std::string& context, const std::string& cap, const std::string& params_raw) {
        contexts.push_back(context);
        caps.push_back(cap);
        params.push_back(params_raw);
        assert(!answers.empty());
        const std::string answer = answers.front();
        answers.erase(answers.begin());
        if (!answer.empty() && answer[0] == '!') throw wrapper::CallError("refused", "x", answer.substr(1));
        return answer;
    }
};

static std::string revision_refused() { return "!{\"reason\":\"revision_mismatch\"}"; }

static void run() {
    const std::string corpus = read_corpus();
    for (const auto& item : array_items(array_field(corpus, "substance"))) {
        const std::string name = string_field(item, "name");
        const std::string data = wrapper::json::raw_field(item, "data", "null");
        const std::string want_error = string_field(item, "error");
        if (!want_error.empty()) {
            expect_cause(name, want_error, [&] { wrapper::classify_substance(data); });
            continue;
        }
        const wrapper::Substance got = wrapper::classify_substance(data);
        const std::string result = object_field(item, "result", "{}");
        if (got.kind != string_field(result, "kind")) fail(name + ": kind");
        if ((got.kind == "inline" ? got.text : got.url) != (got.kind == "inline" ? string_field(result, "text") : string_field(result, "url"))) fail(name + ": value " + got.text + got.url);
    }
    for (const auto& item : array_items(array_field(corpus, "revision"))) {
        const std::string brique = wrapper::json::raw_field(item, "brique", "null");
        // the wanted revision is the last "revision" of the case (the first one is inside the brique)
        const long long want = std::stoll(item.substr(item.rfind("\"revision\":") + 11));
        if (wrapper::revision_of(brique) != want) fail("revision of " + brique);
    }
    for (const auto& item : array_items(array_field(corpus, "lines"))) {
        if (unrepresentable(item)) continue;
        const std::string name = string_field(item, "name");
        const std::string text = string_field(item, "text");
        const std::string want_error = string_field(item, "error");
        if (!want_error.empty()) {
            expect_cause(name, want_error, [&] { wrapper::parse_lines(text); });
            continue;
        }
        const auto got = wrapper::parse_lines(text);
        const auto want = array_items(array_field(item, "lines"));
        if (got.size() != want.size()) fail(name + ": number of lines");
        for (std::size_t i = 0; i < got.size(); ++i) if (wrapper::compact_json(got[i]) != wrapper::compact_json(want[i])) fail(name + ": line " + std::to_string(i));
    }
    for (const auto& item : array_items(array_field(corpus, "dump_lines"))) {
        if (wrapper::dump_lines(array_items(array_field(item, "lines"))) != string_field(item, "text")) fail(string_field(item, "name"));
    }
    if (static_cast<long long>(wrapper::kInlineAppendMax) != wrapper::json::integer_field(object_field(corpus, "append"), "inline_max_bytes")) fail("append limit");
    const std::string pause = object_field(corpus, "guarded_write_pause");
    for (const auto& item : array_items(array_field(pause, "cases"))) {
        const int attempt = static_cast<int>(wrapper::json::integer_field(item, "attempt"));
        const long long base = wrapper::json::integer_field(pause, "base_ms"), cap = wrapper::json::integer_field(pause, "cap_ms");
        if (wrapper::guarded_write_pause_ms(attempt, 1.0, base, cap) != static_cast<double>(wrapper::json::integer_field(item, "max_ms"))) fail("pause");
        if (wrapper::guarded_write_pause_ms(attempt, 0.0, base, cap) != 0.0) fail("pause floor");
    }

    // read inline, then through a lease that goes stale and is read again
    {
        Script script;
        script.answers = {R"({"data":{"kind":"inline","bytes":"{\"a\": 1}"},"brique":{"revision":7},"functional":{"f":1}})"};
        wrapper::MatterClient client([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx");
        const wrapper::MatterView view = client.read("m1");
        assert(view.revision == 7 && wrapper::compact_json(view.content) == "{\"a\":1}" && script.contexts[0] == "/ctx" && script.caps[0] == "matter.read");
    }
    {
        int gets = 0;
        Script script;
        const std::string lease = R"("data":{"kind":"http","http":{"base_url":"http://h","path":"/s","tok":"t"}})";
        script.answers = {"{" + lease + R"(,"brique":{"revision":1}})", "{" + lease + R"(,"brique":{"revision":2}})"};
        wrapper::MatterClient client([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx",
            [&](const std::string& method, const std::string& url, const std::string&) {
                assert(method == "GET" && url == "http://h/s?tok=t");
                return ++gets == 1 ? wrapper::HttpResult{409, ""} : wrapper::HttpResult{200, "{\"big\":true}"};
            });
        const wrapper::MatterView view = client.read("m1");
        assert(view.revision == 2 && gets == 2 && script.caps.size() == 2);
    }
    // read_many: one request, in order; a missing Matter is the error of its answer
    {
        Script script;
        script.answers = {R"({"result":[{"ok":true,"data":{"bytes":"{\"a\": 1}"},"brique":{"revision":3}},{"ok":true,"data":{"bytes":"{\"b\": 2}"},"brique":{"revision":4}}]})",
                          R"({"result":[{"ok":true,"data":{"bytes":"{}"},"brique":{}},{"ok":false,"error":{"code":"not_found","message":"gone"}}]})",
                          R"({"result":[]})"};
        wrapper::MatterClient client([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx");
        const auto views = client.read_many({"m1", "m2"});
        assert(views.size() == 2 && views[0].id == "m1" && views[0].revision == 3 && views[1].revision == 4 && script.caps[0] == "matter.read_batch");
        assert(wrapper::json::array_items(wrapper::json::array_field(script.params[0], "matter_ids")).size() == 2);
        try { client.read_many({"m1", "m2"}); assert(false); } catch (const wrapper::CallError& e) { assert(wrapper::is_not_found(e)); }
        try { client.read_many({"m1"}); assert(false); } catch (const wrapper::CallError& e) { assert(wrapper::cause_of(e) == "batch_size"); }
    }
    // a write is guarded by a decimal revision
    {
        Script script;
        script.answers = {R"({"revision":1790000000000000009})", R"({"revision":10})"};
        wrapper::MatterClient client([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx");
        assert(client.write("m1", "{\"a\":1}", 1790000000000000001LL) == 1790000000000000009LL);
        assert(wrapper::json::string_field(script.params[0], "expected_revision") == "1790000000000000001");
        client.write("m1", "{\"a\":1}");
        assert(!wrapper::json::has_key(script.params[1], "expected_revision"));
    }
    // append inline, through a lease, and a lease that conflicts
    {
        Script script;
        script.answers = {R"({"total_lines":3})"};
        wrapper::MatterClient client([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx");
        assert(client.append_lines("j", {"{\"a\": 1}"}, 2) == 3);
        assert(wrapper::json::string_field(script.params[0], "data") == "{\"a\":1}\n");
        const std::string handle = R"({"http_data":{"base_url":"http://h","path":"/a","tok":"t"}})";
        std::string received;
        long status = 200;
        script.answers = {handle, handle};
        wrapper::MatterClient leased([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx",
            [&](const std::string& method, const std::string&, const std::string& body) { assert(method == "PUT"); received = body; return wrapper::HttpResult{status, ""}; });
        std::vector<std::string> big(300, "{\"k\":\"" + std::string(1000, 'x') + "\"}");
        assert(leased.append_lines("j", big, 5) == 305 && received == wrapper::dump_lines(big));
        status = 409;
        try { leased.append_lines("j", big, 5); assert(false); } catch (const wrapper::CallError& e) { assert(wrapper::is_line_count_mismatch(e)); }
    }
    // read_lines
    {
        Script script;
        script.answers = {R"({"data":{"kind":"inline","bytes":"{\"a\":1}\n{\"b\":2}\n","total_lines":9},"brique":{"revision":4}})"};
        wrapper::MatterClient client([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx");
        const wrapper::LinesView view = client.read_lines("j", 7, 2);
        assert(view.lines.size() == 2 && view.total_lines == 9 && view.revision == 4 && view.offset == 7);
    }
    // update: starts again after a lost race, writes nothing when there is nothing to write, gives up after the attempts
    {
        ::setenv("BRIQUE_CAS_PAUSE_BASE_MS", "0", 1);
        Script script;
        script.answers = {R"({"data":{"bytes":"{\"n\": 1}"},"brique":{"revision":1}})", revision_refused(),
                          R"({"data":{"bytes":"{\"n\": 2}"},"brique":{"revision":2}})", R"({"revision":3})"};
        wrapper::MatterClient client([&](const std::string& c, const std::string& k, const std::string& p) { return script.call(c, k, p); }, "/ctx");
        std::string result;
        const bool wrote = client.update("m1", [](const std::string& content, std::string& out) -> std::optional<std::string> {
            out = content;
            return std::string("{\"n\":9}");
        }, &result);
        assert(wrote && wrapper::json::string_field(script.params.back(), "expected_revision") == "2");
        script.answers = {R"({"data":{"bytes":"{\"n\": 1}"},"brique":{"revision":1}})"};
        assert(!client.update("m1", [](const std::string&, std::string&) -> std::optional<std::string> { return std::nullopt; }));
        ::setenv("BRIQUE_CAS_ATTEMPTS", "3", 1);
        script.answers.clear();
        for (int i = 0; i < 3; ++i) { script.answers.push_back(R"({"data":{"bytes":"{}"},"brique":{"revision":1}})"); script.answers.push_back(revision_refused()); }
        try {
            client.update("m1", [](const std::string&, std::string&) -> std::optional<std::string> { return std::string("{\"x\":1}"); });
            assert(false);
        } catch (const wrapper::CallError& e) { assert(wrapper::cause_of(e).empty() && e.reason() == "concurrency_failure"); }
        ::unsetenv("BRIQUE_CAS_ATTEMPTS");
        ::unsetenv("BRIQUE_CAS_PAUSE_BASE_MS");
    }
}
}  // namespace mattervec

// The settings of the runtime are the shared declarations (vectors/settings.json), and an invalid value is handled as its policy says.
static void run_settings() {
    std::ifstream input("../../vectors/settings.json");
    if (!input.good()) input.open("../../../vectors/settings.json");
    assert(input.good());
    std::stringstream buffer;
    buffer << input.rdbuf();
    const auto declared = wrapper::json::array_items(wrapper::json::array_field(buffer.str(), "settings"));
    const auto& mine = wrapper::declared_settings();
    if (declared.size() > mine.size()) { std::cerr << "settings: " << mine.size() << " declared, " << declared.size() << " in the shared file\n"; std::abort(); }
    for (std::size_t i = 0; i < declared.size(); ++i) {
        const bool same = mine[i].name == wrapper::json::string_field(declared[i], "name") &&
                          mine[i].default_value == wrapper::json::integer_field(declared[i], "default") &&
                          mine[i].minimum == wrapper::json::integer_field(declared[i], "minimum") &&
                          mine[i].strict == (wrapper::json::string_field(declared[i], "policy") == "strict") &&
                          !wrapper::json::string_field(declared[i], "what").empty() && !wrapper::json::string_field(declared[i], "unit").empty();
        if (!same) { std::cerr << "settings: " << mine[i].name << " differs from the shared file\n"; std::abort(); }
    }
    for (const auto& setting : mine) ::unsetenv(setting.name.c_str());
    assert(wrapper::setting_value("BRIQUE_BUSY_ATTEMPTS") == 8 && wrapper::settings_not_default().empty());
    for (const char* bad : {"abc", "1.5", "-3", "0"}) {
        ::setenv("BRIQUE_BUSY_ATTEMPTS", bad, 1);
        assert(wrapper::setting_value("BRIQUE_BUSY_ATTEMPTS") == 8);
    }
    ::setenv("BRIQUE_BUSY_BASE_MS", "0", 1);
    assert(wrapper::setting_value("BRIQUE_BUSY_BASE_MS") == 0);
    for (const char* bad : {"abc", "0", "-1"}) {
        ::setenv("BRIQUE_MAX_CONCURRENCY", bad, 1);
        try { wrapper::strict_setting("BRIQUE_MAX_CONCURRENCY"); assert(false); } catch (const std::runtime_error&) {}
    }
    ::unsetenv("BRIQUE_MAX_CONCURRENCY");
    ::setenv("BRIQUE_CAS_ATTEMPTS", "3", 1);
    ::setenv("BRIQUE_LEASE_RETRIES", "nope", 1);
    std::map<std::string, std::string> sources;
    for (const auto& state : wrapper::describe_settings()) sources[state.setting.name] = state.source;
    assert(sources["BRIQUE_CAS_ATTEMPTS"] == "env" && sources["BRIQUE_LEASE_RETRIES"] == "ignored" && sources["BRIQUE_BUSY_CAP_MS"] == "default");
    const auto changed = wrapper::settings_not_default();
    assert(changed.size() == 2 && changed.at("BRIQUE_CAS_ATTEMPTS") == 3 && changed.at("BRIQUE_BUSY_BASE_MS") == 0);
    ::setenv("BRIQUE_TEST_COUNT", "7", 1);
    wrapper::declare_setting("BRIQUE_TEST_COUNT", 4, 1, "a count");
    wrapper::declare_setting("BRIQUE_TEST_COUNT", 9, 0, "again");
    assert(wrapper::setting_value("BRIQUE_TEST_COUNT") == 7 && wrapper::declared_setting("BRIQUE_TEST_COUNT").default_value == 4);
    assert(wrapper::settings_not_default().count("BRIQUE_TEST_COUNT") == 1);
    ::unsetenv("BRIQUE_TEST_COUNT");
    for (const auto& setting : wrapper::declared_settings()) ::unsetenv(setting.name.c_str());
}

// The classification of the errors of a call is the shared one (vectors/errors.json).
static void run_error_kinds() {
    std::ifstream input("../../vectors/errors.json");
    if (!input.good()) input.open("../../../vectors/errors.json");
    assert(input.good());
    std::stringstream buffer;
    buffer << input.rdbuf();
    const std::string corpus = buffer.str();
    for (const auto& item : wrapper::json::array_items(wrapper::json::array_field(corpus, "cases"))) {
        const std::string error = wrapper::json::raw_field(item, "error", "null");
        const wrapper::CallError failure = wrapper::call_error_from(error);
        if (failure.kind() != wrapper::json::string_field(item, "kind") ||
            failure.outcome_unknown() != wrapper::json::bool_field(item, "outcome_unknown")) {
            std::cerr << "error kind disagrees for " << error << ": " << failure.kind() << "\n";
            std::abort();
        }
    }
    const std::string defaults = wrapper::json::object_field(corpus, "defaults");
    const wrapper::CallError empty = wrapper::call_error_from("null");
    assert(empty.code() == wrapper::json::string_field(defaults, "code") && std::string(empty.what()) == wrapper::json::string_field(defaults, "message"));
}

// The running response is the shared one (vectors/running_response.json), and the keepalive says running at once, then every period, until its scope ends.
static void run_running() {
    std::ifstream input("../../vectors/running_response.json");
    if (!input.good()) input.open("../../../vectors/running_response.json");
    assert(input.good());
    std::stringstream buffer;
    buffer << input.rdbuf();
    using wrapper::json::array_field;
    using wrapper::json::array_items;
    using wrapper::json::object_field;
    using wrapper::json::string_field;
    for (const auto& item : array_items(array_field(buffer.str(), "cases"))) {
        const std::string name = string_field(item, "name");
        const std::string intention = object_field(item, "intention");
        const std::string to = object_field(intention, "to");
        const std::string got = wrapper::running_message(string_field(item, "wrapper_name"), wrapper::json::bool_field(item, "delegated"), string_field(intention, "intention_id"),
                                                         object_field(intention, "from"), string_field(to, "cap"), string_field(to, "type"), string_field(to, "context"), "t");
        const std::string response = object_field(got, "response");
        const std::string want = object_field(item, "response");
        auto same = [&](const std::string& what, const std::string& a, const std::string& b) {
            if (a != b) { std::cerr << "running vector disagrees: " << name << " (" << what << "): " << a << " vs " << b << "\n"; std::abort(); }
        };
        same("intention_id", string_field(response, "intention_id"), string_field(want, "intention_id"));
        same("status", string_field(response, "status"), string_field(want, "status"));
        same("to", wrapper::compact_json(object_field(response, "to")), wrapper::compact_json(object_field(want, "to")));
        for (const char* key : {"context", "cap", "type"}) same(key, string_field(object_field(response, "from"), key), string_field(object_field(want, "from"), key));
        same("identity", wrapper::compact_json(object_field(response, "identity")), wrapper::compact_json(object_field(want, "identity")));
        same("payload", wrapper::compact_json(object_field(response, "payload")), "{}");
        same("kind", string_field(got, "kind"), "response");
    }
    std::atomic<int> count{0};
    {
        wrapper::Keepalive alive([&] { ++count; }, 20);
        assert(count == 1);   // a running at once
        std::this_thread::sleep_for(std::chrono::milliseconds(110));
    }
    const int seen = count;
    assert(seen >= 4);
    std::this_thread::sleep_for(std::chrono::milliseconds(80));
    assert(count == seen);   // the heartbeat stopped with the scope
}

int main() {
    wrapper::WorkScheduler scheduler(2, 8);
    std::mutex mu;
    std::condition_variable cv;
    bool release = false;
    std::vector<std::string> started;
    std::atomic<int> active{0};
    std::atomic<int> maximum{0};

    auto work = [&](const std::string& name) {
        return [&, name] {
            const int current = ++active;
            int seen = maximum.load();
            while (current > seen && !maximum.compare_exchange_weak(seen, current)) {}
            std::unique_lock<std::mutex> lock(mu);
            started.push_back(name);
            cv.notify_all();
            cv.wait(lock, [&] { return release; });
            --active;
        };
    };

    assert(scheduler.submit("a", work("a1")));
    assert(scheduler.submit("a", work("a2")));
    assert(scheduler.submit("b", work("b1")));
    {
        std::unique_lock<std::mutex> lock(mu);
        assert(cv.wait_for(lock, std::chrono::seconds(1), [&] { return started.size() == 2; }));
        assert((started[0] == "a1" || started[1] == "a1"));
        assert((started[0] == "b1" || started[1] == "b1"));
        release = true;
    }
    cv.notify_all();
    scheduler.close();
    assert(maximum.load() == 2);
    assert(started.size() == 3 && started[2] == "a2");

    {   // work submitted unordered runs together, whatever shares its key
        wrapper::WorkScheduler parallel(4, 8);
        std::mutex m;
        std::condition_variable c;
        bool go = false;
        int running = 0;
        auto hold = [&] {
            std::unique_lock<std::mutex> l(m);
            ++running;
            c.notify_all();
            c.wait(l, [&] { return go; });
        };
        assert(parallel.submit("cap", hold, false));
        assert(parallel.submit("cap", hold, false));
        assert(parallel.submit("cap", hold, false));
        {
            std::unique_lock<std::mutex> l(m);
            assert(c.wait_for(l, std::chrono::seconds(1), [&] { return running == 3; }));
            go = true;
        }
        c.notify_all();
        parallel.close();
    }
    {   // a parent that waits for a child call of the same capacity does not wait behind itself
        wrapper::WorkScheduler nested(4, 8);
        std::promise<void> child_done;
        std::promise<void> parent_done;
        auto parent_finished = parent_done.get_future();
        assert(nested.submit("cap", [&] {
            assert(nested.submit("cap", [&] { child_done.set_value(); }, false));
            child_done.get_future().wait();
            parent_done.set_value();
        }, false));
        assert(parent_finished.wait_for(std::chrono::seconds(2)) == std::future_status::ready);
        nested.close();
    }

    {   // as many calls as there are slots, each waiting for a call that needs a slot itself
        const int slots = 3;
        wrapper::WorkScheduler starved(slots, 16);
        std::atomic<int> parents_done{0};
        std::atomic<int> parents_holding{0};
        std::mutex m;
        std::condition_variable c;
        for (int i = 0; i < slots; ++i) {
            assert(starved.submit("cap", [&] {
                ++parents_holding;   // every slot is held by a parent before any child is asked for
                while (parents_holding.load() < slots) std::this_thread::sleep_for(std::chrono::milliseconds(1));
                std::promise<void> child_done;
                auto child_finished = child_done.get_future();
                assert(starved.submit("cap", [&] { child_done.set_value(); }, false));
                {
                    wrapper::SlotYield waiting;   // waiting for the child does not hold a slot
                    child_finished.wait();
                }
                std::lock_guard<std::mutex> l(m);
                ++parents_done;
                c.notify_all();
            }, false));
        }
        {
            std::unique_lock<std::mutex> l(m);
            assert(c.wait_for(l, std::chrono::seconds(3), [&] { return parents_done.load() == slots; }));
        }
        starved.close();
    }

    const std::string raw = R"({"value":{"nested":[1,2]},"name":"ok"})";
    assert(wrapper::json::raw_field(raw, "value") == R"({"nested":[1,2]})");
    assert(wrapper::json::raw_field(raw, "name") == R"("ok")");

    bool released = false;
    {
        wrapper::ParamsView params(
            R"({"document":{"@file":"/tmp/transit","format":"json"},"message":"hello"})",
            [](const std::string& value) {
                if (wrapper::json::has_key(value, "@file")) return std::string(R"({"decoded":true})");
                return value;
            },
            [&](const std::string& value) {
                if (wrapper::json::has_key(value, "@file")) released = true;
            }
        );
        assert(params.value_raw("document") == R"({"decoded":true})");
        assert(params.string_value("message") == "hello");
    }
    assert(!released);  // the only transit reference was consumed

    {
        wrapper::ParamsView params(
            R"({"document":{"@file":"/tmp/transit","format":"json"}})", {},
            [&](const std::string& value) { if (wrapper::json::has_key(value, "@file")) released = true; }
        );
    }
    assert(released);

    // --- Matter references: one wire form, in and out, in every wrapper language ---
    {
        const auto items = wrapper::json::array_items(R"( [ {"a":[1,2]}, "x,y", 3, [4,5], {"s":"]"} ] )");
        assert(items.size() == 5);
        assert(items[0] == R"({"a":[1,2]})" && items[1] == R"("x,y")" && items[2] == "3");
        assert(items[3] == "[4,5]" && items[4] == R"({"s":"]"})");
        assert(wrapper::json::array_items("[]").empty());
    }
    {
        wrapper::MatterRef ref{"/root/a", "report", "read"};
        assert(ref.json() == R"({"context":"/root/a","id":"report","mode":"read"})");
        wrapper::MatterRef bare{"/r", "x", ""};
        assert(bare.json() == R"({"context":"/r","id":"x"})");  // empty mode is omitted
    }
    {
        // incoming matters: repr names the parameter, the id when repr is missing, incomplete entries ignored
        wrapper::ParamsView params(
            R"({"n":1})", {}, {},
            R"([{"context":"/root/demo","id":"state","repr":"src","mode":"read"},)"
            R"({"context":" /root/demo ","id":" other "},)"
            R"({"context":"","id":"orphan","repr":"bad"},)"
            R"({"context":"/root/demo","id":7,"repr":"typed"}])");
        assert(params.has("src") && params.has("other") && !params.has("bad") && !params.has("typed"));
        const wrapper::MatterRef src = params.matter("src");
        assert(src.context == "/root/demo" && src.id == "state" && src.mode == "read");
        const wrapper::MatterRef other = params.matter("other");
        assert(other.context == "/root/demo" && other.id == "other" && other.mode.empty());
        assert(params.value_raw("src") == src.json());  // value_raw agrees with matter()
    }
    {
        // a reference returned in a result payload
        wrapper::ParamsView result(
            R"({"doc":{"context":"/root/a","id":"report","mode":"read"},"bare":{"context":"/r","id":"x"},)"
            R"("extra":{"context":"/r","id":"x","title":"business data"},"no_id":{"context":"/r"},)"
            R"("empty_id":{"context":"/r","id":" "},"bad_mode":{"context":"/r","id":"x","mode":3},)"
            R"("scalar":"text","file":{"@file":"/x/y","format":"json"}})");
        const wrapper::MatterRef doc = result.matter("doc");
        assert(doc.context == "/root/a" && doc.id == "report" && doc.mode == "read");
        assert(result.matter("bare").mode.empty());
        for (const char* name : {"extra", "no_id", "empty_id", "bad_mode", "scalar", "file"}) {
            bool mismatch = false;
            try { result.matter(name); } catch (const wrapper::TransitError& e) { mismatch = e.reason() == "type_mismatch"; }
            assert(mismatch);
        }
        bool missing = false;
        try { result.matter("missing"); } catch (const wrapper::TransitError& e) { missing = e.reason() == "not_found"; }
        assert(missing);
    }
    {
        // an outgoing MatterRef parameter becomes an intention.matters entry named by the parameter
        const std::string arr = wrapper::Runtime::matters_array({{"source", wrapper::MatterRef{"/root/demo", "state", "read"}}});
        assert(arr == R"([{"context":"/root/demo","id":"state","repr":"source","mode":"read"}])");
        bool rejected = false;
        try { wrapper::Runtime::matters_array({{"bad", wrapper::MatterRef{"", "state", ""}}}); }
        catch (const wrapper::TransitError& e) { rejected = e.reason() == "invalid_reference"; }
        assert(rejected);
    }
    {
        std::string message;
        std::string delegation;
        const std::string handle(43, 'A');
        const std::string framed =
            R"({"brique_wrapper_frame":"brique.wrapper/1","message":{"kind":"response","response":{}},"delegation":")" +
            handle + R"("})";
        assert(wrapper::Runtime::decode_wire_message(framed, message, delegation));
        assert(delegation == handle);
        assert(wrapper::json::string_field(message, "kind") == "response");
        const std::string raw_message = R"({"kind":"intention","intention":{}})";
        assert(wrapper::Runtime::decode_wire_message(raw_message, message, delegation));
        assert(delegation.empty() && message == raw_message);
        const std::string extra =
            R"({"brique_wrapper_frame":"brique.wrapper/1","message":{"kind":"response","response":{}},"delegation":")" +
            handle + R"(","unexpected":true})";
        assert(!wrapper::Runtime::decode_wire_message(extra, message, delegation));
        assert(!wrapper::Runtime::decode_wire_message(framed + " true", message, delegation));
    }
    run_frame_vectors();
    listvec::run();
    busyvec::run();
    run_critical_sections();
    run_stats_and_dump();
    mattervec::run();
    run_error_kinds();
    run_settings();
    run_running();
    return 0;
}
