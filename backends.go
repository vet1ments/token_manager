package tokenmanager

import (
	"context"
	"errors"
	"github.com/redis/go-redis/v9"
	"strconv"
	"strings"
	"time"
)

type bUserTokenInfo struct {
	TokenString string // literal TokenString String
	TokenData   string // unmarshal token data
}

type backend interface {
	saveToken(ctx context.Context, token string, value interface{}, expire time.Duration) (bool, error)
	loadToken(ctx context.Context, token string) (string, error)
	deleteToken(ctx context.Context, tokens ...string) error
	isTokenExist(ctx context.Context, token string) (bool, error)

	cleanupUserToken(ctx context.Context, userId string) error
	saveUserToken(ctx context.Context, userId string, genToken func() (string, error), value interface{}, expiresIn time.Duration) (string, error)
	loadUserToken(ctx context.Context, userId string, tokenString string) (*bUserTokenInfo, error)
	loadUserTokenList(ctx context.Context, userId string) ([]*bUserTokenInfo, error)
	deleteUserToken(ctx context.Context, userId string, tokens ...string) error
}

type redisBackend struct {
	backend
	client *redis.Client
}

func (r *redisBackend) getUserTokenKey(userId string) string {
	return strings.Join([]string{
		"USER_TOKENS",
		userId,
	}, ":")
}

func (r *redisBackend) getTokenKey(tokenString string) string {
	return strings.Join([]string{
		"TOKENS",
		tokenString,
	}, ":")
}

func (r *redisBackend) saveToken(ctx context.Context, token string, value interface{}, expire time.Duration) (bool, error) {
	result, err := r.client.SetNX(
		ctx,
		r.getTokenKey(token),
		value,
		expire,
	).Result()
	if err != nil {
		return false, err
	}
	return result, nil
}

func (r *redisBackend) loadToken(ctx context.Context, token string) (string, error) {
	key := r.getTokenKey(token)

	result, err := r.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", ErrTokenNotFound
		}
		return "", err
	}
	return result, nil
}
func (r *redisBackend) deleteToken(ctx context.Context, tokens ...string) error {
	tokensForDelete := make([]string, len(tokens))

	for i, token := range tokens {
		key := r.getTokenKey(token)
		tokensForDelete[i] = key
	}

	return r.client.Unlink(ctx, tokensForDelete...).Err()
}

func (r *redisBackend) extendTokenExpire(ctx context.Context, tokenString string, expire time.Duration) (bool, error) {
	return r.client.Expire(ctx, r.getTokenKey(tokenString), expire).Result()
}

func (r *redisBackend) isTokenExist(ctx context.Context, token string) (bool, error) {
	key := r.getTokenKey(token)

	count, err := r.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if count <= 0 {
		return false, nil
	} else {
		return true, nil
	}
}

func (r *redisBackend) cleanupUserToken(ctx context.Context, userId string) error {
	key := r.getUserTokenKey(userId)

	now := time.Now().UTC().Unix()
	err := r.client.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(now, 10)).Err()
	if err != nil {
		return err
	}
	userTokens, err := r.client.ZRange(ctx, key, 0, -1).Result()
	if err != nil {
		return err
	}

	tokensForDelete := make([]interface{}, 0)
	for _, token := range userTokens {
		ex, err := r.isTokenExist(ctx, token)
		if err != nil {
			return err
		}
		if !ex {
			tokensForDelete = append(tokensForDelete, token)
		}
	}
	return r.client.ZRem(ctx, key, tokensForDelete...).Err()
}

func (r *redisBackend) saveUserToken(ctx context.Context, userId string, genToken func() (string, error), value interface{}, expiresIn time.Duration) (string, error) {
	_ = r.cleanupUserToken(ctx, userId)
	key := r.getUserTokenKey(userId)
	for {
		now := time.Now().UTC()
		expire := now.Add(expiresIn).UTC()

		token, err := genToken()
		if err != nil {
			return "", err
		}

		ok, err := r.saveToken(ctx, token, value, expire.Sub(now))
		if err != nil {
			return "", err
		}
		if ok {
			err = r.client.ZAdd(ctx, key, redis.Z{
				Member: token,
				Score:  float64(expire.Unix()),
			}).Err()
			//r.client.Expire(ctx, key, expire.Sub(time.Now().UTC()))

			if err != nil {
				_ = r.deleteToken(ctx, token)
				return "", err
			}
			return token, nil
		}
	}
}

// user TokenString 내에 없으면 토큰도 지워줌
func (r *redisBackend) loadUserToken(ctx context.Context, userId string, tokenString string) (*bUserTokenInfo, error) {
	_ = r.cleanupUserToken(ctx, userId)
	key := r.getUserTokenKey(userId)

	_, err := r.client.ZScore(ctx, key, tokenString).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			_ = r.deleteToken(ctx, tokenString)
			return nil, ErrTokenNotFound
		}
		return nil, err
	}

	data, err := r.loadToken(ctx, tokenString)
	if err != nil {
		return nil, err
	}
	return &bUserTokenInfo{
		TokenString: tokenString,
		TokenData:   data,
	}, nil
}

func (r *redisBackend) loadUserTokenList(ctx context.Context, userId string) ([]*bUserTokenInfo, error) {
	_ = r.cleanupUserToken(ctx, userId)
	key := r.getUserTokenKey(userId)

	tokenStringList, err := r.client.ZRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	userTokenList := make([]*bUserTokenInfo, 0)
	for _, tokenString := range tokenStringList {
		userToken, err := r.loadUserToken(ctx, userId, tokenString)
		if err != nil {
			continue
		}
		userTokenList = append(userTokenList, userToken)
	}
	return userTokenList, nil
}
