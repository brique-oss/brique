/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

#pragma once

#include <condition_variable>
#include <cstddef>
#include <algorithm>
#include <functional>
#include <future>
#include <memory>
#include <mutex>
#include <string>
#include <stdexcept>
#include <thread>
#include <set>
#include <unordered_map>
#include <vector>

namespace wrapper {

class WorkScheduler;

// A snapshot of the execution slots, for a diagnostic or a metric.
struct SchedulerStats {
    std::size_t max_concurrency = 0, queue_limit = 0, free_slots = 0, active = 0, pending = 0, queued = 0;
    std::size_t refused = 0;   // works refused because the queue was full, since the start
    std::size_t ordered_chains = 0, locks = 0;
    bool accepting = true;
};

// The execution slot the work running on this thread holds, if it runs under a scheduler.
struct WorkLease {
    WorkScheduler* scheduler = nullptr;
    bool holding = false;
};

inline thread_local WorkLease* current_work_lease = nullptr;

class WorkScheduler {
public:
    WorkScheduler(std::size_t max_concurrency, std::size_t queue_limit)
        : max_concurrency_(max_concurrency), queue_limit_(queue_limit) {
        if (max_concurrency_ == 0 || queue_limit_ == 0) {
            throw std::runtime_error("scheduler limits must be positive");
        }
    }

    ~WorkScheduler() { close(); }

    // With ordered=true the work runs after the work already queued under the same key; with
    // ordered=false it only waits for a free slot, whatever else is queued under its key.
    bool submit(const std::string& key, std::function<void()> work, bool ordered = true) {
        std::shared_future<void> previous;
        auto completed = std::make_shared<std::promise<void>>();
        const auto done = completed->get_future().share();
        auto token = std::make_shared<int>(0);
        {
            std::lock_guard<std::mutex> lock(mu_);
            if (!accepting_ || pending_ >= max_concurrency_ + queue_limit_) {
                ++refused_;
                return false;
            }
            if (ordered) {
                const auto it = tails_.find(key);
                if (it != tails_.end()) {
                    previous = it->second.future;
                }
                tails_[key] = Tail{done, token};
            }
            ++pending_;
        }
        std::thread([this, key, previous, token, ordered, completed, work = std::move(work)]() mutable {
            if (previous.valid()) {
                previous.wait();
            }
            {
                std::unique_lock<std::mutex> lock(mu_);
                cv_.wait(lock, [this] { return active_ < max_concurrency_; });
                ++active_;
            }
            WorkLease lease{this, true};
            current_work_lease = &lease;
            try {
                work();
            } catch (...) {
                // Runtime work records its own error; the scheduler keeps progressing.
            }
            current_work_lease = nullptr;
            {
                std::lock_guard<std::mutex> lock(mu_);
                if (lease.holding) {
                    --active_;
                }
                --pending_;
                if (ordered) {
                    const auto it = tails_.find(key);
                    if (it != tails_.end() && it->second.token == token) {
                        tails_.erase(it);
                    }
                }
            }
            completed->set_value();
            cv_.notify_all();
        }).detach();
        return true;
    }

    // Give an execution slot back, and take one again (in turn). Used through SlotYield.
    void release_slot() {
        {
            std::lock_guard<std::mutex> lock(mu_);
            --active_;
        }
        cv_.notify_all();
    }

    void acquire_slot() {
        std::unique_lock<std::mutex> lock(mu_);
        cv_.wait(lock, [this] { return active_ < max_concurrency_; });
        ++active_;
    }

    SchedulerStats stats() {
        SchedulerStats out;
        {
            std::lock_guard<std::mutex> lock(mu_);
            out.max_concurrency = max_concurrency_;
            out.queue_limit = queue_limit_;
            out.free_slots = max_concurrency_ > active_ ? max_concurrency_ - active_ : 0;
            out.active = active_;
            out.pending = pending_;
            out.queued = pending_ > active_ ? pending_ - active_ : 0;
            out.refused = refused_;
            out.ordered_chains = tails_.size();
            out.accepting = accepting_;
        }
        std::lock_guard<std::mutex> lock(critical_mu_);
        out.locks = critical_.size();
        return out;
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
    std::size_t refused_ = 0;
    std::unordered_map<std::string, Tail> tails_;

    struct CriticalEntry {
        bool held = false;
        std::size_t holders = 0;   // the sections that hold the key or wait for it
    };
    std::mutex critical_mu_;
    std::condition_variable critical_cv_;
    std::unordered_map<std::string, CriticalEntry> critical_;

    friend class CriticalSection;
};

// Gives the execution slot of the work running on this thread back while the thread waits for the
// answer to a call it made, and takes it back, in turn, when it goes out of scope. Without it, as
// many calls as there are slots, each waiting for a call that needs a slot itself, would wait for
// each other for ever. Outside scheduled work this does nothing.
class SlotYield {
public:
    SlotYield() : lease_(current_work_lease) {
        if (lease_ != nullptr && lease_->holding) {
            lease_->holding = false;
            lease_->scheduler->release_slot();
            yielded_ = true;
        }
    }
    ~SlotYield() {
        if (yielded_) {
            lease_->scheduler->acquire_slot();
            lease_->holding = true;
        }
    }
    SlotYield(const SlotYield&) = delete;
    SlotYield& operator=(const SlotYield&) = delete;

private:
    WorkLease* lease_;
    bool yielded_ = false;
};

// Holds the lock of every key for the length of its scope: a short "read, compute, guarded write" on shared state.
//
// The keys are taken in sorted order, so two sections that name the same keys in a different order cannot wait for each other. A wait
// for a lock that is taken does not hold an execution slot: without that, as many sections waiting for the same key as there are slots
// would stop the wrapper, the one that holds the lock needing a slot itself to make progress. A lock is local to this process and
// reduces conflicts; the guard on the write stays the guarantee. Do not hold it across a long wait.
class CriticalSection {
public:
    CriticalSection(WorkScheduler& scheduler, const std::vector<std::string>& keys) : scheduler_(scheduler) {
        const std::set<std::string> unique(keys.begin(), keys.end());   // sorted and without repetition
        for (const std::string& key : unique) {
            take(key);
            held_.push_back(key);
        }
    }
    ~CriticalSection() { release(); }
    CriticalSection(const CriticalSection&) = delete;
    CriticalSection& operator=(const CriticalSection&) = delete;

private:
    void take(const std::string& key) {
        {
            std::lock_guard<std::mutex> lock(scheduler_.critical_mu_);
            auto& entry = scheduler_.critical_[key];
            ++entry.holders;
            if (!entry.held) {
                entry.held = true;
                return;
            }
        }
        // the key is taken: wait for it without holding an execution slot (the slot is taken back after the mutex is let go)
        SlotYield waiting_gives_its_slot_back;
        std::unique_lock<std::mutex> lock(scheduler_.critical_mu_);
        scheduler_.critical_cv_.wait(lock, [&] { return !scheduler_.critical_[key].held; });
        scheduler_.critical_[key].held = true;
    }

    void release() {
        {
            std::lock_guard<std::mutex> lock(scheduler_.critical_mu_);
            for (auto it = held_.rbegin(); it != held_.rend(); ++it) {
                auto found = scheduler_.critical_.find(*it);
                if (found == scheduler_.critical_.end()) continue;
                found->second.held = false;
                if (--found->second.holders == 0) scheduler_.critical_.erase(found);
            }
        }
        held_.clear();
        scheduler_.critical_cv_.notify_all();
    }

    WorkScheduler& scheduler_;
    std::vector<std::string> held_;
};

}  // namespace wrapper
