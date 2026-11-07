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
    return 0;
}
