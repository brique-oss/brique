/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

#pragma once

#include <condition_variable>
#include <cstddef>
#include <functional>
#include <future>
#include <memory>
#include <mutex>
#include <string>
#include <stdexcept>
#include <thread>
#include <unordered_map>

namespace wrapper {

class WorkScheduler {
public:
    WorkScheduler(std::size_t max_concurrency, std::size_t queue_limit)
        : max_concurrency_(max_concurrency), queue_limit_(queue_limit) {
        if (max_concurrency_ == 0 || queue_limit_ == 0) {
            throw std::runtime_error("scheduler limits must be positive");
        }
    }

    ~WorkScheduler() { close(); }

    bool submit(const std::string& key, std::function<void()> work) {
        std::shared_future<void> previous;
        auto completed = std::make_shared<std::promise<void>>();
        const auto done = completed->get_future().share();
        auto token = std::make_shared<int>(0);
        {
            std::lock_guard<std::mutex> lock(mu_);
            if (!accepting_ || pending_ >= max_concurrency_ + queue_limit_) {
                return false;
            }
            const auto it = tails_.find(key);
            if (it != tails_.end()) {
                previous = it->second.future;
            }
            tails_[key] = Tail{done, token};
            ++pending_;
        }
        std::thread([this, key, previous, token, completed, work = std::move(work)]() mutable {
            if (previous.valid()) {
                previous.wait();
            }
            {
                std::unique_lock<std::mutex> lock(mu_);
                cv_.wait(lock, [this] { return active_ < max_concurrency_; });
                ++active_;
            }
            try {
                work();
            } catch (...) {
                // Runtime work records its own error; the scheduler keeps progressing.
            }
            {
                std::lock_guard<std::mutex> lock(mu_);
                --active_;
                --pending_;
                const auto it = tails_.find(key);
                if (it != tails_.end() && it->second.token == token) {
                    tails_.erase(it);
                }
            }
            completed->set_value();
            cv_.notify_all();
        }).detach();
        return true;
    }

    std::size_t queued() const {
        std::lock_guard<std::mutex> lock(mu_);
        return pending_ > active_ ? pending_ - active_ : 0;
    }

    void close() {
        std::unique_lock<std::mutex> lock(mu_);
        accepting_ = false;
        cv_.wait(lock, [this] { return pending_ == 0; });
    }

private:
    struct Tail {
        std::shared_future<void> future;
        std::shared_ptr<int> token;
    };
    std::size_t max_concurrency_;
    std::size_t queue_limit_;
    mutable std::mutex mu_;
    std::condition_variable cv_;
    std::size_t pending_ = 0;
    std::size_t active_ = 0;
    bool accepting_ = true;
    std::unordered_map<std::string, Tail> tails_;
};

}  // namespace wrapper
