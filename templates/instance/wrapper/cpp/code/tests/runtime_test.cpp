// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

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
    return 0;
}
