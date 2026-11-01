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
    return 0;
}
