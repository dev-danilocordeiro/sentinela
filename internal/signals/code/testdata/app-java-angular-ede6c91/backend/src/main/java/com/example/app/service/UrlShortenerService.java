package com.example.app.service;

import com.example.app.dto.ShortenUrlRequest;
import com.example.app.dto.ShortenUrlResponse;
import com.example.app.dto.UrlAnalyticsResponse;
import com.example.app.dto.UrlStatsResponse;
import com.example.app.exception.AliasAlreadyExistsException;
import com.example.app.model.ClickEvent;
import com.example.app.model.UrlData;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.data.redis.core.RedisTemplate;
import org.springframework.stereotype.Service;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.TreeMap;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ThreadLocalRandom;
import java.util.stream.Collectors;

@Service
@Slf4j
public class UrlShortenerService {

    private final RedisTemplate<String, Object> redisTemplate;
    private final Clock clock;
    private final String baseUrl;
    private final int shortCodeLength;
    private final int maxGenerationAttempts;
    private final int cacheTtlMinutes;

    private final Map<String, UrlData> urlMappings = new ConcurrentHashMap<>();

    public UrlShortenerService(
            RedisTemplate<String, Object> redisTemplate,
            Clock clock,
            @Value("${app.base-url}") String baseUrl,
            @Value("${app.short-code.length}") int shortCodeLength,
            @Value("${app.short-code.max-attempts}") int maxGenerationAttempts,
            @Value("${app.cache.ttl-minutes}") int cacheTtlMinutes) {
        this.redisTemplate = redisTemplate;
        this.clock = clock;
        this.baseUrl = baseUrl;
        this.shortCodeLength = shortCodeLength;
        this.maxGenerationAttempts = maxGenerationAttempts;
        this.cacheTtlMinutes = cacheTtlMinutes;
    }

    private static final String BASE_62_CHARS = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

    public ShortenUrlResponse shortenUrl(ShortenUrlRequest request, String clientIp) {
        String customAlias = request.getCustomAlias();
        UrlData urlData;

        if (customAlias == null || customAlias.isBlank()) {
            urlData = storeWithGeneratedShortCode(request, clientIp);
        } else {
            urlData = newUrlData(customAlias.trim(), request, clientIp);
            // putIfAbsent makes check-and-insert atomic, so two requests racing for one alias can't both win.
            if (urlMappings.putIfAbsent(urlData.getShortCode(), urlData) != null) {
                throw new AliasAlreadyExistsException(urlData.getShortCode());
            }
        }

        String shortCode = urlData.getShortCode();
        cacheUrl(shortCode, request.getOriginalUrl(), request.getExpiresAt());

        log.info("Created short URL: {} -> {}", shortCode, request.getOriginalUrl());

        return ShortenUrlResponse.builder()
                .shortUrl(buildShortUrl(shortCode))
                .shortCode(shortCode)
                .originalUrl(request.getOriginalUrl())
                .createdAt(urlData.getCreatedAt())
                .expiresAt(urlData.getExpiresAt())
                .build();
    }

    private UrlData newUrlData(String shortCode, ShortenUrlRequest request, String clientIp) {
        return UrlData.builder()
                .originalUrl(request.getOriginalUrl())
                .shortCode(shortCode)
                .expiresAt(request.getExpiresAt())
                .createdAt(Instant.now(clock))
                .createdBy(clientIp)
                .active(true)
                .build();
    }

    private String buildShortUrl(String shortCode) {
        String normalizedBaseUrl = baseUrl.endsWith("/") ? baseUrl.substring(0, baseUrl.length() -1) : baseUrl;
        return normalizedBaseUrl + "/api/" + shortCode;
    }

    private void cacheUrl(String shortCode, String originalUrl, Instant expiresAt) {
        // A cache hit skips the expiry check, so the entry must never outlive the link.
        Duration ttl = Duration.ofMinutes(cacheTtlMinutes);
        if (expiresAt != null) {
            Duration untilExpiry = Duration.between(Instant.now(clock), expiresAt);
            if (untilExpiry.isNegative() || untilExpiry.isZero()) {
                return;
            }
            if (untilExpiry.compareTo(ttl) < 0) {
                ttl = untilExpiry;
            }
        }

        try {
            redisTemplate.opsForValue().set("url:" + shortCode, originalUrl, ttl);
        }  catch (Exception e) {
            log.warn("Failed to cache url for: {}:{}", shortCode, e.getMessage());
        }
    }

    private UrlData storeWithGeneratedShortCode(ShortenUrlRequest request, String clientIp) {
        for (int attempt = 0; attempt < maxGenerationAttempts; attempt++) {
            UrlData urlData = newUrlData(generateRandomBase62(), request, clientIp);
            if (urlMappings.putIfAbsent(urlData.getShortCode(), urlData) == null) {
                return urlData;
            }
        }

        throw new IllegalStateException("Failed to generate unique short code after " + maxGenerationAttempts + " attempts");
    }

    private String generateRandomBase62() {
        StringBuilder sb = new StringBuilder(shortCodeLength);
        for (int i = 0; i < shortCodeLength; i++) {
            int index = ThreadLocalRandom.current().nextInt(BASE_62_CHARS.length());

            sb.append(BASE_62_CHARS.charAt(index));
        }

        return sb.toString();
    }

    public Optional<String> getOriginalUrl(String shortCode) {
        String cachedUrl = getCachedUrl(shortCode);
        if (cachedUrl != null) {
            return Optional.of(cachedUrl);
        }

        UrlData urlData = urlMappings.get(shortCode);
        if (urlData != null && urlData.isActive()) {
            if(isExpired(urlData)) {
                urlData.setActive(false);
                return Optional.empty();
            }
            cacheUrl(shortCode, urlData.getOriginalUrl(), urlData.getExpiresAt());
            return Optional.of(urlData.getOriginalUrl());
        }
        return Optional.empty();
    }

    private boolean isExpired(UrlData urlData) {
        return urlData.getExpiresAt() != null && urlData.getExpiresAt().isBefore(Instant.now(clock));
    }

    private String getCachedUrl(String shortCode) {
        try {
            return (String) redisTemplate.opsForValue().get("url:" + shortCode);
        } catch (Exception e) {
            log.warn("Failed to get cached url for: {}:{}", shortCode, e.getMessage());
            return null;
        }
    }

    public void recordClick(String shortCode, String clientIp, String userAgent, String referer) {
        UrlData urlData = urlMappings.get(shortCode);
        if (urlData != null && urlData.isActive()) {
            urlData.recordClick(ClickEvent.builder()
                    .timestamp(Instant.now(clock))
                    .ipAddress(clientIp)
                    .userAgent(userAgent)
                    .referer(referer)
                    .build());
            log.debug("Clicked URL: {} -> {}", shortCode, urlData.getOriginalUrl());
        }
    }

    public Optional<UrlStatsResponse> getUrlStats(String shortCode) {
        UrlData urlData = urlMappings.get(shortCode);
        if (urlData == null) {
            return Optional.empty();
        }

        return Optional.of(
                UrlStatsResponse.builder()
                        .shortCode(shortCode)
                        .createdAt(urlData.getCreatedAt())
                        .expiresAt(urlData.getExpiresAt())
                        .originalUrl(urlData.getOriginalUrl())
                        .clickCount(urlData.getClickCount())
                        .active(urlData.isActive())
                        .createdBy(urlData.getCreatedBy())
                        .build()
        );
    }

    public Optional<UrlAnalyticsResponse> getUrlAnalytics(String shortCode) {

        UrlData urlData = urlMappings.get(shortCode);
        if (urlData == null) {
            return Optional.empty();
        }

        // Snapshot once so every aggregate below is computed from the same set of clicks.
        List<ClickEvent> clickEvents = List.copyOf(urlData.getClickEvents());

        Map<String, Integer> clicksByReferer = clickEvents.stream()
                .filter(c -> c.getReferer() != null)
                .collect(Collectors.groupingBy(
                        ClickEvent::getReferer, Collectors.summingInt( e -> 1)
                ));

        // Hours and days are bucketed in the server's time zone (TZ in docker-compose).
        // TreeMap keeps them in chronological order; "%02d" makes "09:00" sort before "10:00".
        Map<String, Integer> clicksByHour = clickEvents.stream()
                .collect(Collectors.groupingBy(
                        c -> "%02d:00".formatted(c.getTimestamp().atZone(clock.getZone()).getHour()),
                        TreeMap::new,
                        Collectors.summingInt( e -> 1)
                ));

        Map<String, Integer> clicksByDay = clickEvents.stream()
                .collect(Collectors.groupingBy(
                        c -> c.getTimestamp().atZone(clock.getZone()).toLocalDate().toString(),
                        TreeMap::new,
                        Collectors.summingInt( e -> 1)
                ));

        List<ClickEvent> recentClicks = clickEvents.stream()
                .sorted(( a, b) -> b.getTimestamp().compareTo(a.getTimestamp()))
                .limit(10)
                .toList();


        return Optional.of(
                UrlAnalyticsResponse.builder()
                        .shortCode(shortCode)
                        .createdAt(urlData.getCreatedAt())
                        .expiresAt(urlData.getExpiresAt())
                        .originalUrl(urlData.getOriginalUrl())
                        .totalClicks(clickEvents.size())
                        .recentClicks(recentClicks)
                        .clicksByDay(clicksByDay)
                        .clicksByHour(clicksByHour)
                        .clicksByReferer(clicksByReferer)
                        .build()
        );
    }

    public boolean deleteUrl(String shortCode) {
        UrlData urlData = urlMappings.get(shortCode);
        if (urlData != null && urlData.isActive()) {
            urlData.setActive(false);
            deleteCacheUrl(shortCode);
            log.info("Deleted URL: {} -> {}", shortCode, urlData.getOriginalUrl());
            return true;
        }
        return false;
    }

    private void deleteCacheUrl(String shortCode) {
        try {
            redisTemplate.delete("url:" + shortCode);
        } catch (Exception e) {
            log.warn("Failed to delete URL: {} -> {}", shortCode, e.getMessage());
        }
    }

    public void cleanupExpiredUrls() {
        int cleanedCount = 0;
        Instant now = Instant.now(clock);

        for (Map.Entry<String, UrlData> entry : urlMappings.entrySet()) {
            UrlData urlData = entry.getValue();
            if (urlData.getExpiresAt() != null && urlData.getExpiresAt().isBefore(now) && urlData.isActive()) {
                urlData.setActive(false);
                deleteCacheUrl(entry.getKey());
                cleanedCount++;
            }
        }

        if (cleanedCount > 0) {
            log.info("Deactivated {} expired URLs ({} URLs in total)", cleanedCount, urlMappings.size());
        }
    }
}
